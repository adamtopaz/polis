package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/adamtopaz/polis/internal/model"
)

type Client struct {
	baseURL       string
	operatorToken string
	workerToken   string
	http          *http.Client
}

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 35 * time.Second},
	}
}

func NewOperator(baseURL, token string) *Client {
	client := New(baseURL)
	client.operatorToken = token
	return client
}

func NewWorker(baseURL, token string) *Client {
	client := New(baseURL)
	client.workerToken = token
	return client
}

func (c *Client) ListAgents(ctx context.Context) ([]model.Agent, error) {
	var response struct {
		Items []model.Agent `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/agents", c.operatorToken, nil, &response)
	return response.Items, err
}

func (c *Client) GetAgent(ctx context.Context, id string) (model.Agent, error) {
	var agent model.Agent
	err := c.do(ctx, http.MethodGet, "/v1/agents/"+id, c.operatorToken, nil, &agent)
	return agent, err
}

func (c *Client) SetState(ctx context.Context, id string, state model.State) (model.Agent, error) {
	var agent model.Agent
	err := c.do(ctx, http.MethodPut, "/v1/agents/"+id+"/state", c.operatorToken, map[string]any{"state": state}, &agent)
	return agent, err
}

func (c *Client) SendControlMessage(ctx context.Context, id, sender string, body json.RawMessage) (model.Message, error) {
	var message model.Message
	err := c.do(ctx, http.MethodPost, "/v1/agents/"+id+"/messages", c.operatorToken, map[string]any{"sender": sender, "body": body}, &message)
	return message, err
}

func (c *Client) Events(ctx context.Context, id string) ([]model.Event, error) {
	path := "/v1/events"
	if id != "" {
		path = "/v1/agents/" + id + "/events"
	}
	var response struct {
		Items []model.Event `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, path, c.operatorToken, nil, &response)
	return response.Items, err
}

func (c *Client) Acquire(ctx context.Context, agentID, workerID string, ttl, wait time.Duration) (model.Lease, bool, error) {
	var lease model.Lease
	status, err := c.doStatus(ctx, http.MethodPost, "/v1/worker/acquire", c.workerToken, map[string]any{
		"agent_id": agentID, "worker_id": workerID, "ttl_seconds": int64(ttl.Seconds()), "wait_seconds": int64(wait.Seconds()),
	}, &lease)
	if status == http.StatusNoContent && err == nil {
		return model.Lease{}, false, nil
	}
	return lease, err == nil, err
}

func (c *Client) Heartbeat(ctx context.Context, token string, ttl time.Duration) (model.Heartbeat, error) {
	var heartbeat model.Heartbeat
	err := c.do(ctx, http.MethodPost, "/v1/worker/heartbeat", token, map[string]any{"ttl_seconds": int64(ttl.Seconds())}, &heartbeat)
	return heartbeat, err
}

func (c *Client) Exited(ctx context.Context, token, detail string) error {
	return c.do(ctx, http.MethodPost, "/v1/worker/exited", token, map[string]any{"detail": detail}, nil)
}

func (c *Client) Self(ctx context.Context, token string) (model.Agent, error) {
	var agent model.Agent
	err := c.do(ctx, http.MethodGet, "/v1/self", token, nil, &agent)
	return agent, err
}

func (c *Client) Messages(ctx context.Context, token string) ([]model.Message, error) {
	return c.WaitMessages(ctx, token, 0)
}

func (c *Client) WaitMessages(ctx context.Context, token string, wait time.Duration) ([]model.Message, error) {
	var response struct {
		Items []model.Message `json:"items"`
	}
	path := "/v1/self/messages"
	if wait > 0 {
		path += "?wait_seconds=" + fmt.Sprint(int64(wait/time.Second))
	}
	err := c.do(ctx, http.MethodGet, path, token, nil, &response)
	return response.Items, err
}

func (c *Client) AckMessages(ctx context.Context, token string, through uint64) error {
	return c.do(ctx, http.MethodPost, "/v1/self/messages/ack", token, map[string]any{"through": through}, nil)
}

func (c *Client) SendMessage(ctx context.Context, token, to string, body json.RawMessage) (model.Message, error) {
	var message model.Message
	err := c.do(ctx, http.MethodPost, "/v1/self/messages", token, map[string]any{"to": to, "body": body}, &message)
	return message, err
}

func (c *Client) Journal(ctx context.Context, token, kind string, data json.RawMessage) (model.Event, error) {
	var event model.Event
	err := c.do(ctx, http.MethodPost, "/v1/self/journal", token, map[string]any{"kind": kind, "data": data}, &event)
	return event, err
}

func (c *Client) do(ctx context.Context, method, path, token string, input, output any) error {
	_, err := c.doStatus(ctx, method, path, token, input, output)
	return err
}

func (c *Client) doStatus(ctx context.Context, method, path, token string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiError); err != nil {
			return response.StatusCode, fmt.Errorf("polis returned %s", response.Status)
		}
		return response.StatusCode, &Error{Status: response.StatusCode, Message: apiError.Error}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return response.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return response.StatusCode, nil
}
