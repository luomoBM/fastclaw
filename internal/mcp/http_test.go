package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestHTTPConnectSendsInitializedBeforeRequests drives HTTPClient against a
// strict fake MCP server that rejects any request arriving before the
// notifications/initialized notification.
func TestHTTPConnectSendsInitializedBeforeRequests(t *testing.T) {
	var mu sync.Mutex
	initialized := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if msg.ID == nil { // notification, no reply body
			if msg.Method == "notifications/initialized" {
				mu.Lock()
				initialized = true
				mu.Unlock()
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		mu.Lock()
		ok := initialized
		mu.Unlock()
		if msg.Method != "initialize" && !ok {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, "strict server: request before notifications/initialized")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"strict","version":"0"}}}`, *msg.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}}`, *msg.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"unknown method"}}`, *msg.ID)
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, nil)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := client.ListTools()
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}
