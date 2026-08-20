package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStdioConnectSendsInitializedBeforeRequests drives StdioClient against
// a strict fake MCP server (a re-exec'd helper process) that exits when a
// request arrives before notifications/initialized — the behaviour that
// breaks rmcp-based servers such as mempal.
func TestStdioConnectSendsInitializedBeforeRequests(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		strictStdioServer()
		return
	}

	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	client := NewStdioClient(os.Args[0],
		[]string{"-test.run=TestStdioConnectSendsInitializedBeforeRequests", "--"}, nil)
	defer client.Close()

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

// strictStdioServer mimics an rmcp-style server over stdin/stdout: any
// request before the initialized notification kills the process.
func strictStdioServer() {
	sc := bufio.NewScanner(os.Stdin)
	initialized := false
	for sc.Scan() {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue // skip non-JSON lines
		}

		if msg.ID == nil { // notification, no reply
			if msg.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"strict","version":"0"}}}`+"\n",
				*msg.ID)
		case "tools/list":
			if !initialized {
				fmt.Fprintln(os.Stderr, "strict server: request before notifications/initialized")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}}`+"\n",
				*msg.ID)
		default:
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"unknown method"}}`+"\n",
				*msg.ID)
		}
	}
}

// TestStdioSurvivesServerInitiatedRequest — servers may send their own
// requests (ping, roots/list) whose ids occupy the same small-integer
// space as ours. A request line unmarshals fine into jsonRPCResponse
// (its method ignored); pre-fix, one whose id collided with the
// pending request was returned AS the response with a nil result —
// surfacing as "unexpected end of JSON input" or, for strict servers
// that wait for a reply and exit, the production
// "process exited without response". The client must skip (and answer)
// server-initiated messages instead of matching them.
func TestStdioSurvivesServerInitiatedRequest(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_PING") == "1" {
		pingingStdioServer()
		return
	}

	t.Setenv("GO_WANT_HELPER_PROCESS_PING", "1")
	client := NewStdioClient(os.Args[0],
		[]string{"-test.run=TestStdioSurvivesServerInitiatedRequest", "--"}, nil)
	defer client.Close()

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

// pingingStdioServer answers normally but, on each request, first
// sends its OWN ping request carrying the SAME id the client just used
// — a guaranteed collision.
func pingingStdioServer() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			continue // a response (e.g. our ping reply) — ignore
		}
		if msg.ID == nil {
			continue // notification
		}
		// Server-initiated request with the colliding id, then the
		// real response.
		fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"method":"ping","params":{}}`+"\n", *msg.ID)
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"pinger","version":"0"}}}`+"\n",
				*msg.ID)
		case "tools/list":
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}}`+"\n",
				*msg.ID)
		default:
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"unknown method"}}`+"\n",
				*msg.ID)
		}
	}
}

// TestStdioRepliesToStringIDServerRequests — JSON-RPC permits string
// request ids, and pre-fix the first unmarshal (into the int-id
// response type) dropped such lines outright; a server that waits for
// its reply before answering ours hung the client until process
// death. The reply must echo the string id verbatim.
func TestStdioRepliesToStringIDServerRequests(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_STRING_ID") == "1" {
		stringIDStdioServer()
		return
	}

	t.Setenv("GO_WANT_HELPER_PROCESS_STRING_ID", "1")
	client := NewStdioClient(os.Args[0],
		[]string{"-test.run=TestStdioRepliesToStringIDServerRequests", "--"}, nil)
	defer client.Close()

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

// stringIDStdioServer answers a request only after the client has
// replied to its string-id ping — servers that serialize on their own
// pending requests stall without that reply.
func stringIDStdioServer() {
	// Watchdog: pre-fix the client never replies; exit instead of
	// hanging the test so sendRequest surfaces "process exited
	// without response".
	time.AfterFunc(5*time.Second, func() {
		fmt.Fprintln(os.Stderr, "string-id server: timed out waiting for ping reply")
		os.Exit(1)
	})
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			continue // a response (e.g. our ping reply) — ignore
		}
		if msg.ID == nil {
			continue // notification
		}
		id := *msg.ID
		// Server-initiated request with a STRING id, then hold the
		// answer until the client's reply echoes it.
		fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":"srv-1","method":"ping","params":{}}`)
		if !sc.Scan() {
			os.Exit(1)
		}
		if !strings.Contains(string(sc.Bytes()), `"id":"srv-1"`) {
			fmt.Fprintln(os.Stderr, "string-id server: reply did not echo the string id")
			os.Exit(1)
		}
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"strider","version":"0"}}}`+"\n",
				id)
		case "tools/list":
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}}`+"\n",
				id)
		default:
			fmt.Fprintf(os.Stdout,
				`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"unknown method"}}`+"\n",
				id)
		}
	}
}
