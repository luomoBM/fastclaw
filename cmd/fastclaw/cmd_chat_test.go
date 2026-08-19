package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/cliclient"
)

func TestSelectAgent(t *testing.T) {
	agents := []cliclient.Agent{
		{ID: "agt_1", Name: "Coder"},
		{ID: "agt_2", Name: "Researcher"},
	}

	got, err := selectAgent(agents, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "agt_1" {
		t.Fatalf("selected %q, want agt_1", got.ID)
	}

	if _, err := selectAgent(agents, "missing"); err == nil {
		t.Fatal("expected missing agent error")
	}
}

func TestSelectAgentDefaultsToFirstCreated(t *testing.T) {
	first := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	latest := first.Add(time.Hour)
	agents := []cliclient.Agent{
		{ID: "agt_latest", Name: "Latest", CreatedAt: latest},
		{ID: "agt_first", Name: "First", CreatedAt: first},
	}

	got, err := selectAgent(agents, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "agt_first" {
		t.Fatalf("selected %q, want first-created agent", got.ID)
	}
}

func TestPlainStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_delta\",\"data\":{\"delta\":\"Hello\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_delta\",\"data\":{\"delta\":\" world\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content\",\"data\":{\"content\":\"Hello world\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	c := cliclient.NewWithHTTPClient(server.URL, "test-token", server.Client())
	var out bytes.Buffer
	if err := plainStream(context.Background(), c, "agt_1", "session-1", "hi", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Hello world\n" {
		t.Fatalf("output = %q", got)
	}
}

type observingWriter struct {
	mu     sync.Mutex
	writes []string
}

func (w *observingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes = append(w.writes, string(p))
	w.mu.Unlock()
	return len(p), nil
}

func TestPlainStreamWritesDeltasAndToolProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"type\":\"content_delta\",\"data\":{\"delta\":\"first\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"tool_call\",\"data\":{\"id\":\"call-1\",\"name\":\"exec\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"tool_result\",\"data\":{\"id\":\"call-1\",\"result\":\"command completed successfully\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n\n"))
	}))
	defer server.Close()

	out := &observingWriter{}
	c := cliclient.NewWithHTTPClient(server.URL, "test-token", server.Client())
	if err := plainStream(context.Background(), c, "agt_1", "session-1", "hi", out); err != nil {
		t.Fatal(err)
	}
	out.mu.Lock()
	writes := append([]string(nil), out.writes...)
	out.mu.Unlock()
	if len(writes) < 4 || writes[0] != "first" {
		t.Fatalf("expected first delta to be its own immediate write, got %#v", writes)
	}
	joined := strings.Join(writes, "")
	if !strings.Contains(joined, "↳ exec") || !strings.Contains(joined, "✓ exec command completed successfully") {
		t.Fatalf("tool progress missing from %q", joined)
	}
	if strings.Contains(joined, "\x1b[") {
		t.Fatalf("non-terminal output contains ANSI escapes: %q", joined)
	}
}

func TestPlainStreamErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"data\":{\"message\":\"provider exploded\"}}\n\n"))
	}))
	defer server.Close()

	c := cliclient.NewWithHTTPClient(server.URL, "test-token", server.Client())
	var out bytes.Buffer
	err := plainStream(context.Background(), c, "agt_1", "session-1", "hi", &out)
	if err == nil || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("expected provider error, got %v", err)
	}
}
