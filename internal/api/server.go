package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/adamtopaz/polis/internal/model"
	"github.com/adamtopaz/polis/internal/store"
)

type Server struct {
	store         *store.Store
	log           *slog.Logger
	operatorToken string
}

func New(st *store.Store, logger *slog.Logger, operatorToken string) *Server {
	return &Server{store: st, log: logger, operatorToken: operatorToken}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("POST /v1/agents", s.requireOperator(http.HandlerFunc(s.createAgent)))
	mux.Handle("GET /v1/agents", s.requireOperator(http.HandlerFunc(s.listAgents)))
	mux.Handle("GET /v1/agents/{id}", s.requireOperator(http.HandlerFunc(s.getAgent)))
	mux.Handle("PUT /v1/agents/{id}/state", s.requireOperator(http.HandlerFunc(s.setState)))
	mux.Handle("POST /v1/agents/{id}/messages", s.requireOperator(http.HandlerFunc(s.sendControlMessage)))
	mux.Handle("GET /v1/agents/{id}/events", s.requireOperator(http.HandlerFunc(s.agentEvents)))
	mux.Handle("GET /v1/events", s.requireOperator(http.HandlerFunc(s.events)))
	mux.HandleFunc("POST /v1/worker/acquire", s.acquire)
	mux.HandleFunc("POST /v1/worker/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /v1/worker/exited", s.exited)
	mux.HandleFunc("GET /v1/self", s.self)
	mux.HandleFunc("GET /v1/self/messages", s.selfMessages)
	mux.HandleFunc("POST /v1/self/messages", s.sendSelfMessage)
	mux.HandleFunc("POST /v1/self/messages/ack", s.ackMessages)
	mux.HandleFunc("POST /v1/self/schedule", s.scheduleSelfMessage)
	mux.HandleFunc("POST /v1/self/spawn", s.spawn)
	mux.HandleFunc("POST /v1/self/journal", s.journal)
	return s.recoverAndLog(mux)
}

func (s *Server) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := bearer(r)
		if s.operatorToken == "" || len(provided) != len(s.operatorToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.operatorToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "valid operator token required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type createAgentRequest struct {
	ID      string   `json:"id"`
	Charter string   `json:"charter"`
	Runtime []string `json:"runtime"`
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var request createAgentRequest
	if !decode(w, r, &request) {
		return
	}
	agent, err := s.store.CreateAgent(request.ID, request.Charter, request.Runtime, "operator")
	respond(w, http.StatusCreated, agent, err)
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.ListAgents()
	respond(w, http.StatusOK, map[string]any{"items": items}, err)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	agent, err := s.store.GetAgent(r.PathValue("id"))
	respond(w, http.StatusOK, agent, err)
}

func (s *Server) setState(w http.ResponseWriter, r *http.Request) {
	var request struct {
		State model.State `json:"state"`
	}
	if !decode(w, r, &request) {
		return
	}
	agent, err := s.store.SetState(r.PathValue("id"), request.State, "operator")
	respond(w, http.StatusOK, agent, err)
}

func (s *Server) sendControlMessage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Sender string          `json:"sender"`
		Body   json.RawMessage `json:"body"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Sender == "" {
		request.Sender = "operator"
	}
	message, err := s.store.SendMessage(r.PathValue("id"), request.Sender, request.Body)
	respond(w, http.StatusCreated, message, err)
}

func (s *Server) agentEvents(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Events(r.PathValue("id"), queryLimit(r))
	respond(w, http.StatusOK, map[string]any{"items": items}, err)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Events("", queryLimit(r))
	respond(w, http.StatusOK, map[string]any{"items": items}, err)
}

func (s *Server) acquire(w http.ResponseWriter, r *http.Request) {
	var request struct {
		WorkerID    string `json:"worker_id"`
		TTLSeconds  int64  `json:"ttl_seconds"`
		WaitSeconds int64  `json:"wait_seconds"`
	}
	if !decode(w, r, &request) {
		return
	}
	ttl := time.Duration(request.TTLSeconds) * time.Second
	wait := time.Duration(request.WaitSeconds) * time.Second
	if wait < 0 || wait > 30*time.Second {
		writeError(w, errors.New("wait_seconds must be between 0 and 30"))
		return
	}
	deadline := time.Now().Add(wait)
	for {
		lease, err := s.store.Acquire(request.WorkerID, ttl)
		if err == nil {
			respond(w, http.StatusOK, lease, nil)
			return
		}
		if !errors.Is(err, store.ErrNoAgent) {
			writeError(w, err)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TTLSeconds int64 `json:"ttl_seconds"`
	}
	if !decode(w, r, &request) {
		return
	}
	heartbeat, err := s.store.Heartbeat(bearer(r), time.Duration(request.TTLSeconds)*time.Second)
	respond(w, http.StatusOK, heartbeat, err)
}

func (s *Server) exited(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Detail string `json:"detail"`
	}
	if !decode(w, r, &request) {
		return
	}
	err := s.store.Exit(bearer(r), request.Detail)
	respond(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) self(w http.ResponseWriter, r *http.Request) {
	agent, err := s.store.Self(bearer(r))
	respond(w, http.StatusOK, agent, err)
}

func (s *Server) selfMessages(w http.ResponseWriter, r *http.Request) {
	waitSeconds, err := queryWaitSeconds(r)
	if err != nil {
		writeError(w, err)
		return
	}
	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
	for {
		items, err := s.store.Messages(bearer(r), queryLimit(r))
		if err != nil || len(items) > 0 || waitSeconds == 0 {
			respond(w, http.StatusOK, map[string]any{"items": items}, err)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			respond(w, http.StatusOK, map[string]any{"items": items}, nil)
			return
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) sendSelfMessage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		To   string          `json:"to"`
		Body json.RawMessage `json:"body"`
	}
	if !decode(w, r, &request) {
		return
	}
	message, err := s.store.SendMessageAs(bearer(r), request.To, request.Body)
	respond(w, http.StatusCreated, message, err)
}

func (s *Server) ackMessages(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Through uint64 `json:"through"`
	}
	if !decode(w, r, &request) {
		return
	}
	err := s.store.AckMessages(bearer(r), request.Through)
	respond(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

func (s *Server) scheduleSelfMessage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AfterSeconds int64           `json:"after_seconds"`
		Body         json.RawMessage `json:"body"`
	}
	if !decode(w, r, &request) {
		return
	}
	const maxScheduleSeconds = int64(100 * 365 * 24 * 60 * 60)
	if request.AfterSeconds < 1 || request.AfterSeconds > maxScheduleSeconds {
		writeError(w, errors.New("after_seconds must be between 1 and 3153600000"))
		return
	}
	deliverAt := time.Now().UTC().Add(time.Duration(request.AfterSeconds) * time.Second)
	message, err := s.store.ScheduleMessage(bearer(r), deliverAt, request.Body)
	respond(w, http.StatusCreated, message, err)
}

func (s *Server) spawn(w http.ResponseWriter, r *http.Request) {
	parentID, err := s.store.AgentIDForToken(bearer(r))
	if err != nil {
		writeError(w, err)
		return
	}
	var request createAgentRequest
	if !decode(w, r, &request) {
		return
	}
	agent, err := s.store.CreateAgent(request.ID, request.Charter, request.Runtime, "agent:"+parentID)
	respond(w, http.StatusCreated, agent, err)
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.store.Journal(bearer(r), request.Kind, request.Data)
	respond(w, http.StatusCreated, event, err)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, map[string]string{"status": "ok"}, nil)
}

func (s *Server) recoverAndLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("request panic", "error", recovered, "method", r.Method, "path", r.URL.Path)
				writeError(w, errors.New("internal server error"))
			}
			s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}()
		next.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, errors.New("request body must contain one JSON value"))
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrInvalidLease):
		status = http.StatusUnauthorized
	case strings.Contains(err.Error(), "already exists"):
		status = http.StatusConflict
	case err.Error() == "internal server error":
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func bearer(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

func queryLimit(r *http.Request) int {
	value, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return value
}

func queryWaitSeconds(r *http.Request) (int, error) {
	value := r.URL.Query().Get("wait_seconds")
	if value == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 || seconds > 30 {
		return 0, errors.New("wait_seconds must be between 0 and 30")
	}
	return seconds, nil
}
