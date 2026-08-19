package cliclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamCallbackAndErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_delta\",\"data\":{\"delta\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte(": keepalive comment\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	c := NewWithHTTPClient(server.URL, "tok", server.Client())
	var types []string
	err := c.Stream(context.Background(), "a", "s", "msg", func(ev Event) {
		types = append(types, ev.Type)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(types, ",") != "content_delta,done" {
		t.Fatalf("event types = %v", types)
	}
}

func TestStreamTruncatedWithoutDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_delta\",\"data\":{\"delta\":\"hi\"}}\n\n"))
	}))
	defer server.Close()

	c := NewWithHTTPClient(server.URL, "tok", server.Client())
	err := c.Stream(context.Background(), "a", "s", "msg", func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("expected truncated-stream error, got %v", err)
	}
}

func TestStreamImagesSendsImageURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message   string   `json:"message"`
			ImageURLs []string `json:"imageUrls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Message != "look" || len(body.ImageURLs) != 1 || body.ImageURLs[0] != "data:image/png;base64,eA==" {
			t.Fatalf("request body = %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	c := NewWithHTTPClient(server.URL, "tok", server.Client())
	if err := c.StreamImages(context.Background(), "a", "s", "look", []string{"data:image/png;base64,eA=="}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
}

func TestSteerStatusMapping(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()

	c := NewWithHTTPClient(server.URL, "tok", server.Client())
	buffered, err := c.Steer(context.Background(), "a", "s", "msg")
	if err != nil || !buffered {
		t.Fatalf("200 → buffered=true, got %v %v", buffered, err)
	}
	status = http.StatusConflict
	buffered, err = c.Steer(context.Background(), "a", "s", "msg")
	if err != nil || buffered {
		t.Fatalf("409 → buffered=false, got %v %v", buffered, err)
	}
}

func TestNewSessionID(t *testing.T) {
	a, b := NewSessionID(), NewSessionID()
	if !strings.HasPrefix(a, "cli-") {
		t.Fatalf("session ID %q lacks cli prefix", a)
	}
	if a == b {
		t.Fatalf("session IDs unexpectedly equal: %q", a)
	}
}
