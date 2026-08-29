package store

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/adamtopaz/polis/internal/model"
	bolt "go.etcd.io/bbolt"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrNoAgent             = errors.New("no agent ready")
	ErrInvalidLease        = errors.New("invalid or expired lease")
	errScheduledMessageDue = errors.New("scheduled message due")
	idPattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

var (
	bucketMeta      = []byte("meta")
	bucketAgents    = []byte("agents")
	bucketLeases    = []byte("leases")
	bucketMessages  = []byte("messages")
	bucketScheduled = []byte("scheduled_messages")
	bucketEvents    = []byte("events")
)

type agentRecord struct {
	ID             string      `json:"id"`
	Charter        string      `json:"charter"`
	Runtime        []string    `json:"runtime"`
	State          model.State `json:"state"`
	WakeAt         *time.Time  `json:"wake_at,omitempty"`
	LeaseOwner     string      `json:"lease_owner,omitempty"`
	LeaseToken     string      `json:"lease_token,omitempty"`
	LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
	MailboxCursor  uint64      `json:"mailbox_cursor,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type Store struct {
	db  *bolt.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketAgents, bucketLeases, bucketMessages, bucketScheduled, bucketEvents} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return tx.Bucket(bucketMeta).Put([]byte("schema"), []byte("2"))
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateAgent(id, charter string, runtime []string, actor string) (model.Agent, error) {
	if id == "" {
		id = "agent-" + randomHex(12)
	}
	if !idPattern.MatchString(id) {
		return model.Agent{}, fmt.Errorf("agent id must match %s", idPattern)
	}
	if charter == "" {
		return model.Agent{}, errors.New("charter is required")
	}
	if len(runtime) == 0 || runtime[0] == "" {
		return model.Agent{}, errors.New("runtime command is required")
	}
	now := s.now()
	record := agentRecord{ID: id, Charter: charter, Runtime: append([]string(nil), runtime...), State: model.StateActive, CreatedAt: now, UpdatedAt: now}
	err := s.db.Update(func(tx *bolt.Tx) error {
		agents := tx.Bucket(bucketAgents)
		if agents.Get([]byte(id)) != nil {
			return fmt.Errorf("agent %q already exists", id)
		}
		if err := putRecord(agents, id, record); err != nil {
			return err
		}
		return appendEvent(tx, model.Event{AgentID: id, Actor: actor, Kind: "agent.created", Data: mustJSON(map[string]any{"runtime": runtime}), CreatedAt: now})
	})
	return publicAgent(record, now), err
}

func (s *Store) GetAgent(id string) (model.Agent, error) {
	var record agentRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return getRecord(tx.Bucket(bucketAgents), id, &record)
	})
	return publicAgent(record, s.now()), err
}

func (s *Store) ListAgents() ([]model.Agent, error) {
	now := s.now()
	items := make([]model.Agent, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAgents).ForEach(func(_, value []byte) error {
			var record agentRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			items = append(items, publicAgent(record, now))
			return nil
		})
	})
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, err
}

func (s *Store) SetState(id string, state model.State, actor string) (model.Agent, error) {
	if !state.Valid() {
		return model.Agent{}, fmt.Errorf("invalid state %q", state)
	}
	now := s.now()
	var record agentRecord
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getRecord(tx.Bucket(bucketAgents), id, &record); err != nil {
			return err
		}
		oldState := record.State
		if oldState == model.StateTerminated && state != model.StateTerminated {
			return errors.New("terminated agents cannot be restarted")
		}
		clearLease(tx, &record)
		record.State = state
		record.UpdatedAt = now
		if state == model.StateActive {
			record.WakeAt = nil
		}
		if err := putRecord(tx.Bucket(bucketAgents), id, record); err != nil {
			return err
		}
		return appendEvent(tx, model.Event{AgentID: id, Actor: actor, Kind: "agent.state_changed", Data: mustJSON(map[string]any{"from": oldState, "to": state}), CreatedAt: now})
	})
	return publicAgent(record, now), err
}

func (s *Store) Acquire(worker string, ttl time.Duration) (model.Lease, error) {
	if worker == "" {
		return model.Lease{}, errors.New("worker id is required")
	}
	if ttl < 5*time.Second || ttl > 10*time.Minute {
		return model.Lease{}, errors.New("lease duration must be between 5s and 10m")
	}
	now := s.now()
	var lease model.Lease
	err := s.db.Update(func(tx *bolt.Tx) error {
		agents := tx.Bucket(bucketAgents)
		cursor := agents.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record agentRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.State != model.StateActive || (record.WakeAt != nil && record.WakeAt.After(now)) {
				continue
			}
			if record.LeaseExpiresAt != nil && record.LeaseExpiresAt.After(now) {
				continue
			}
			clearLease(tx, &record)
			expires := now.Add(ttl)
			token := randomHex(32)
			record.LeaseOwner = worker
			record.LeaseToken = token
			record.LeaseExpiresAt = &expires
			record.WakeAt = nil
			record.UpdatedAt = now
			if err := putRecord(agents, record.ID, record); err != nil {
				return err
			}
			if err := tx.Bucket(bucketLeases).Put([]byte(token), []byte(record.ID)); err != nil {
				return err
			}
			if err := appendEvent(tx, model.Event{AgentID: record.ID, Actor: "worker:" + worker, Kind: "incarnation.started", CreatedAt: now}); err != nil {
				return err
			}
			lease = model.Lease{Token: token, Agent: publicAgent(record, now), ExpiresAt: expires}
			return nil
		}
		return ErrNoAgent
	})
	return lease, err
}

func (s *Store) Heartbeat(token string, ttl time.Duration) (model.Heartbeat, error) {
	if ttl < 5*time.Second || ttl > 10*time.Minute {
		return model.Heartbeat{}, errors.New("lease duration must be between 5s and 10m")
	}
	now := s.now()
	var heartbeat model.Heartbeat
	err := s.db.Update(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		if record.State != model.StateActive {
			clearLease(tx, &record)
			if err := putRecord(tx.Bucket(bucketAgents), record.ID, record); err != nil {
				return err
			}
			heartbeat.Continue = false
			return nil
		}
		expires := now.Add(ttl)
		record.LeaseExpiresAt = &expires
		record.UpdatedAt = now
		if err := putRecord(tx.Bucket(bucketAgents), record.ID, record); err != nil {
			return err
		}
		heartbeat = model.Heartbeat{Continue: true, ExpiresAt: expires}
		return nil
	})
	return heartbeat, err
}

func (s *Store) Exit(token, detail string) error {
	now := s.now()
	return s.db.Update(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		worker := record.LeaseOwner
		clearLease(tx, &record)
		if record.State == model.StateActive {
			delay := time.Second
			if detail != "" {
				delay = 5 * time.Second
			}
			wake := now.Add(delay)
			record.WakeAt = &wake
		}
		record.UpdatedAt = now
		if err := putRecord(tx.Bucket(bucketAgents), record.ID, record); err != nil {
			return err
		}
		return appendEvent(tx, model.Event{AgentID: record.ID, Actor: "worker:" + worker, Kind: "incarnation.exited", Data: mustJSON(map[string]any{"detail": detail}), CreatedAt: now})
	})
}

func (s *Store) Self(token string) (model.Agent, error) {
	now := s.now()
	var record agentRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		record, err = recordForToken(tx, token, now)
		return err
	})
	return publicAgent(record, now), err
}

func (s *Store) SendMessage(agentID, sender string, body json.RawMessage) (model.Message, error) {
	if !json.Valid(body) {
		return model.Message{}, errors.New("message body must be valid JSON")
	}
	now := s.now()
	var message model.Message
	err := s.db.Update(func(tx *bolt.Tx) error {
		var record agentRecord
		if err := getRecord(tx.Bucket(bucketAgents), agentID, &record); err != nil {
			return err
		}
		if record.State == model.StateTerminated {
			return errors.New("cannot message a terminated agent")
		}
		var err error
		message, err = appendMessage(tx, agentID, sender, body, now)
		if err != nil {
			return err
		}
		if record.State == model.StateActive && record.WakeAt != nil {
			record.WakeAt = nil
			record.UpdatedAt = now
			if err := putRecord(tx.Bucket(bucketAgents), agentID, record); err != nil {
				return err
			}
		}
		return appendEvent(tx, model.Event{AgentID: agentID, Actor: sender, Kind: "message.sent", Data: mustJSON(map[string]any{"message_id": message.ID}), CreatedAt: now})
	})
	return message, err
}

func (s *Store) ScheduleMessage(token string, deliverAt time.Time, body json.RawMessage) (model.ScheduledMessage, error) {
	if !json.Valid(body) {
		return model.ScheduledMessage{}, errors.New("message body must be valid JSON")
	}
	now := s.now()
	deliverAt = deliverAt.UTC()
	if !deliverAt.After(now) {
		return model.ScheduledMessage{}, errors.New("delivery time must be in the future")
	}
	var scheduled model.ScheduledMessage
	err := s.db.Update(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		queue, err := tx.Bucket(bucketScheduled).CreateBucketIfNotExists([]byte(record.ID))
		if err != nil {
			return err
		}
		id, err := queue.NextSequence()
		if err != nil {
			return err
		}
		scheduled = model.ScheduledMessage{
			ID: id, AgentID: record.ID, Sender: "agent:" + record.ID,
			Body: append(json.RawMessage(nil), body...), DeliverAt: deliverAt, CreatedAt: now,
		}
		encoded, err := json.Marshal(scheduled)
		if err != nil {
			return err
		}
		if err := queue.Put(sequenceKey(id), encoded); err != nil {
			return err
		}
		return appendEvent(tx, model.Event{
			AgentID: record.ID, Actor: scheduled.Sender, Kind: "message.scheduled",
			Data: mustJSON(map[string]any{"schedule_id": id, "deliver_at": deliverAt}), CreatedAt: now,
		})
	})
	return scheduled, err
}

func (s *Store) SendMessageAs(token, agentID string, body json.RawMessage) (model.Message, error) {
	agent, err := s.Self(token)
	if err != nil {
		return model.Message{}, err
	}
	return s.SendMessage(agentID, "agent:"+agent.ID, body)
}

func (s *Store) Messages(token string, limit int) ([]model.Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	now := s.now()
	due := false
	err := s.db.View(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		due, err = hasDueMessages(tx, record.ID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if due {
		if err := s.db.Update(func(tx *bolt.Tx) error {
			record, err := recordForToken(tx, token, now)
			if err != nil {
				return err
			}
			return promoteDueMessages(tx, record.ID, now)
		}); err != nil {
			return nil, err
		}
	}
	items := make([]model.Message, 0)
	err = s.db.View(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		mailbox := tx.Bucket(bucketMessages).Bucket([]byte(record.ID))
		if mailbox == nil {
			return nil
		}
		cursor := mailbox.Cursor()
		for key, value := cursor.Seek(sequenceKey(record.MailboxCursor + 1)); key != nil && len(items) < limit; key, value = cursor.Next() {
			var message model.Message
			if err := json.Unmarshal(value, &message); err != nil {
				return err
			}
			items = append(items, message)
		}
		return nil
	})
	return items, err
}

func hasDueMessages(tx *bolt.Tx, agentID string, now time.Time) (bool, error) {
	queue := tx.Bucket(bucketScheduled).Bucket([]byte(agentID))
	if queue == nil {
		return false, nil
	}
	err := queue.ForEach(func(_, value []byte) error {
		var scheduled model.ScheduledMessage
		if err := json.Unmarshal(value, &scheduled); err != nil {
			return err
		}
		if !scheduled.DeliverAt.After(now) {
			return errScheduledMessageDue
		}
		return nil
	})
	if errors.Is(err, errScheduledMessageDue) {
		return true, nil
	}
	return false, err
}

func (s *Store) AckMessages(token string, through uint64) error {
	now := s.now()
	return s.db.Update(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		if through < record.MailboxCursor {
			return errors.New("mailbox cursor cannot move backwards")
		}
		mailbox := tx.Bucket(bucketMessages).Bucket([]byte(record.ID))
		if mailbox == nil || mailbox.Get(sequenceKey(through)) == nil {
			return errors.New("message does not exist")
		}
		record.MailboxCursor = through
		record.UpdatedAt = now
		return putRecord(tx.Bucket(bucketAgents), record.ID, record)
	})
}

func (s *Store) Journal(token, kind string, data json.RawMessage) (model.Event, error) {
	if kind == "" {
		return model.Event{}, errors.New("event kind is required")
	}
	if len(data) > 0 && !json.Valid(data) {
		return model.Event{}, errors.New("event data must be valid JSON")
	}
	now := s.now()
	var event model.Event
	err := s.db.Update(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err != nil {
			return err
		}
		event = model.Event{AgentID: record.ID, Actor: "agent:" + record.ID, Kind: kind, Data: append(json.RawMessage(nil), data...), CreatedAt: now}
		if err := appendEvent(tx, event); err != nil {
			return err
		}
		event.ID = tx.Bucket(bucketEvents).Sequence()
		return nil
	})
	return event, err
}

func (s *Store) Events(agentID string, limit int) ([]model.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	items := make([]model.Event, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketEvents).Cursor()
		for key, value := cursor.Last(); key != nil && len(items) < limit; key, value = cursor.Prev() {
			var event model.Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			if agentID == "" || event.AgentID == agentID {
				items = append(items, event)
			}
		}
		return nil
	})
	return items, err
}

func (s *Store) AgentIDForToken(token string) (string, error) {
	now := s.now()
	var id string
	err := s.db.View(func(tx *bolt.Tx) error {
		record, err := recordForToken(tx, token, now)
		if err == nil {
			id = record.ID
		}
		return err
	})
	return id, err
}

func publicAgent(record agentRecord, now time.Time) model.Agent {
	phase := string(record.State)
	leaseOwner := ""
	var leaseExpiresAt *time.Time
	if record.State == model.StateActive {
		switch {
		case record.LeaseExpiresAt != nil && record.LeaseExpiresAt.After(now):
			phase = "running"
			leaseOwner = record.LeaseOwner
			leaseExpiresAt = record.LeaseExpiresAt
		case record.WakeAt != nil && record.WakeAt.After(now):
			phase = "backoff"
		default:
			phase = "ready"
		}
	}
	return model.Agent{
		ID: record.ID, Charter: record.Charter, Runtime: append([]string(nil), record.Runtime...), State: record.State,
		Phase: phase, WakeAt: record.WakeAt, LeaseOwner: leaseOwner, LeaseExpiresAt: leaseExpiresAt,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func appendMessage(tx *bolt.Tx, agentID, sender string, body json.RawMessage, now time.Time) (model.Message, error) {
	mailbox, err := tx.Bucket(bucketMessages).CreateBucketIfNotExists([]byte(agentID))
	if err != nil {
		return model.Message{}, err
	}
	id, err := mailbox.NextSequence()
	if err != nil {
		return model.Message{}, err
	}
	message := model.Message{ID: id, AgentID: agentID, Sender: sender, Body: append(json.RawMessage(nil), body...), CreatedAt: now}
	encoded, err := json.Marshal(message)
	if err != nil {
		return model.Message{}, err
	}
	if err := mailbox.Put(sequenceKey(id), encoded); err != nil {
		return model.Message{}, err
	}
	return message, nil
}

func promoteDueMessages(tx *bolt.Tx, agentID string, now time.Time) error {
	queue := tx.Bucket(bucketScheduled).Bucket([]byte(agentID))
	if queue == nil {
		return nil
	}
	cursor := queue.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var scheduled model.ScheduledMessage
		if err := json.Unmarshal(value, &scheduled); err != nil {
			return err
		}
		if scheduled.DeliverAt.After(now) {
			continue
		}
		message, err := appendMessage(tx, agentID, scheduled.Sender, scheduled.Body, now)
		if err != nil {
			return err
		}
		if err := appendEvent(tx, model.Event{
			AgentID: agentID, Actor: scheduled.Sender, Kind: "message.delivered",
			Data: mustJSON(map[string]any{"message_id": message.ID, "schedule_id": scheduled.ID}), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return nil
}

func recordForToken(tx *bolt.Tx, token string, now time.Time) (agentRecord, error) {
	var record agentRecord
	if token == "" {
		return record, ErrInvalidLease
	}
	id := tx.Bucket(bucketLeases).Get([]byte(token))
	if id == nil {
		return record, ErrInvalidLease
	}
	if err := getRecord(tx.Bucket(bucketAgents), string(id), &record); err != nil {
		return record, err
	}
	if record.LeaseToken != token || record.LeaseExpiresAt == nil || !record.LeaseExpiresAt.After(now) {
		return agentRecord{}, ErrInvalidLease
	}
	return record, nil
}

func clearLease(tx *bolt.Tx, record *agentRecord) {
	if record.LeaseToken != "" {
		_ = tx.Bucket(bucketLeases).Delete([]byte(record.LeaseToken))
	}
	record.LeaseOwner = ""
	record.LeaseToken = ""
	record.LeaseExpiresAt = nil
}

func getRecord(bucket *bolt.Bucket, id string, target *agentRecord) error {
	value := bucket.Get([]byte(id))
	if value == nil {
		return ErrNotFound
	}
	return json.Unmarshal(value, target)
}

func putRecord(bucket *bolt.Bucket, id string, value agentRecord) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(id), encoded)
}

func appendEvent(tx *bolt.Tx, event model.Event) error {
	bucket := tx.Bucket(bucketEvents)
	id, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	event.ID = id
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return bucket.Put(sequenceKey(id), encoded)
}

func sequenceKey(value uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, value)
	return key
}

func randomHex(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("read system randomness: %v", err))
	}
	return hex.EncodeToString(value)
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
