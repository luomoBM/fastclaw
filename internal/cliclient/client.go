// Package cliclient is the HTTP client the fastclaw terminal uses to
// talk to a gateway. It wraps the same /api/chat endpoints the web
// dashboard calls; the TUI and the one-shot --query path both consume
// the event-callback Stream so rendering stays out of the transport.
package cliclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Agent is one row from GET /api/agents.
type Agent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"createdAt"`
}

// Session is one row from GET /api/chat/sessions.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	UpdatedAt int64  `json:"updatedAt"`
}

// HistoryMessage is one archived turn from GET /api/chat/history.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Event is one SSE frame from POST /api/chat/stream. Type is one of
// content, content_delta, tool_call, tool_result, steer, error, done,
// turn_pending, subagent_progress (see internal/agent.ChatEvent).
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Str returns a string field from the event payload.
func (e Event) Str(key string) string {
	s, _ := e.Data[key].(string)
	return s
}

// Client talks to one gateway with one API key.
type Client struct {
	baseURL string
	apiKey  string
	httpc   *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, httpc: &http.Client{}}
}

// NewWithHTTPClient is for tests that need the httptest server's client.
func NewWithHTTPClient(baseURL, apiKey string, hc *http.Client) *Client {
	c := New(baseURL, apiKey)
	c.httpc = hc
	return c
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.httpc.Do(req)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var payload struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.getJSON(ctx, "/api/agents", &payload); err != nil {
		return nil, err
	}
	return payload.Agents, nil
}

// Sessions returns the agent's sessions, most recently updated first.
func (c *Client) Sessions(ctx context.Context, agentID string) ([]Session, error) {
	var payload struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.getJSON(ctx, "/api/chat/sessions?agentId="+url.QueryEscape(agentID), &payload); err != nil {
		return nil, err
	}
	sort.SliceStable(payload.Sessions, func(i, j int) bool {
		return payload.Sessions[i].UpdatedAt > payload.Sessions[j].UpdatedAt
	})
	return payload.Sessions, nil
}

func (c *Client) History(ctx context.Context, agentID, sessionID string) ([]HistoryMessage, error) {
	var payload struct {
		History []HistoryMessage `json:"history"`
	}
	path := "/api/chat/history?agentId=" + url.QueryEscape(agentID) + "&sessionId=" + url.QueryEscape(sessionID)
	if err := c.getJSON(ctx, path, &payload); err != nil {
		return nil, err
	}
	return payload.History, nil
}

// Steer buffers a message into an in-flight turn. Returns false when the
// gateway has no active turn for the session (the caller should send the
// message as a normal turn instead).
func (c *Client) Steer(ctx context.Context, agentID, sessionID, message string) (bool, error) {
	resp, err := c.request(ctx, http.MethodPost, "/api/chat/steer", map[string]any{
		"agentId": agentID, "sessionId": sessionID, "message": message,
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusConflict:
		return false, nil
	default:
		return false, responseError(resp)
	}
}

func (c *Client) RenameSession(ctx context.Context, sessionID, title string) error {
	resp, err := c.request(ctx, http.MethodPut, "/api/chat/sessions/"+url.PathEscape(sessionID), map[string]any{"title": title})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	return nil
}

// Stream POSTs one user turn and invokes on for every SSE event until
// the turn finishes. A gateway "error" event becomes the returned error;
// "done" ends the stream with nil. The callback runs on the reader
// goroutine — TUI callers should forward events to their event loop.
func (c *Client) Stream(ctx context.Context, agentID, sessionID, message string, on func(Event)) error {
	return c.StreamImages(ctx, agentID, sessionID, message, nil, on)
}

// StreamImages is Stream with vision image attachments. imageURLs may contain
// HTTPS URLs or data:image/* URLs; the latter is what the terminal clipboard
// path uses so no temporary local file has to be exposed to the gateway.
func (c *Client) StreamImages(ctx context.Context, agentID, sessionID, message string, imageURLs []string, on func(Event)) error {
	resp, err := c.request(ctx, http.MethodPost, "/api/chat/stream", map[string]any{
		"agentId": agentID, "sessionId": sessionID, "message": message, "imageUrls": imageURLs,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		switch event.Type {
		case "error":
			on(event)
			return errors.New(event.Str("message"))
		case "done":
			on(event)
			return nil
		default:
			on(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Stream ended without a done event: connection dropped mid-turn.
	// The gateway keeps the turn running server-side; surface that
	// instead of pretending the turn completed.
	return errors.New("stream closed before the turn finished (the agent keeps running; reopen the session to see the reply)")
}

func responseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
		return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, payload.Error)
	}
	return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

// NewSessionID mints a fresh terminal session key.
func NewSessionID() string {
	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("cli-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(suffix[:]))
}
