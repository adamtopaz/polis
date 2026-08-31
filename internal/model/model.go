package model

import (
	"encoding/json"
	"time"
)

type State string

const (
	StateActive     State = "active"
	StatePaused     State = "paused"
	StateTerminated State = "terminated"
)

func (s State) Valid() bool {
	return s == StateActive || s == StatePaused || s == StateTerminated
}

type Agent struct {
	ID             string     `json:"id"`
	Charter        string     `json:"charter"`
	Runtime        []string   `json:"runtime"`
	State          State      `json:"state"`
	Phase          string     `json:"phase"`
	WakeAt         *time.Time `json:"wake_at,omitempty"`
	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Message struct {
	ID        uint64          `json:"id"`
	AgentID   string          `json:"agent_id"`
	Sender    string          `json:"sender"`
	Body      json.RawMessage `json:"body"`
	CreatedAt time.Time       `json:"created_at"`
}

type Event struct {
	ID        uint64          `json:"id"`
	AgentID   string          `json:"agent_id,omitempty"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Lease struct {
	Token     string    `json:"token"`
	Agent     Agent     `json:"agent"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Heartbeat struct {
	Continue  bool      `json:"continue"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}
