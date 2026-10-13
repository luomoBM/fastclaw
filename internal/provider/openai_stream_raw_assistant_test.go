package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChatStreamRawAssistantIncludesToolCalls pins the bug we hit in
// production: streamChatToResponse uses ChatStream for every model
// call (tool iterations included), so the assistant message
// persisted from a tool-emitting turn writes m.RawAssistant from
// the stream's final chunk. toAPIMessages later prefers
// m.RawAssistant over rebuilding from m.ToolCalls, so if
// RawAssistant omits "tool_calls" the next API call ships:
//
//	[..., assistant {role, content, reasoning_content}, tool, tool, ...]
//
// — and OpenAI 400s with "Messages with role 'tool' must be a
// response to a preceding message with 'tool_calls'".
//
// This test mocks an OpenAI SSE stream that emits structured tool
// calls then [DONE]; the resulting RawAssistant must round-trip the
// tool_calls block verbatim. Without the fix in ChatStream's final
// chunk this assertion fails immediately.
func TestChatStreamRawAssistantIncludesToolCalls(t *testing.T) {
	// SSE script: model returns one tool_call (read_file IDENTITY.md)
	// and finishes with [DONE]. Closely mirrors what DeepSeek/OpenAI
	// actually send during a tool-emitting turn.
	chunks := []string{
		`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"IDENTITY.md\"}"}}]}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	sr, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil, "test-model", 1024, 0.1)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var (
		gotToolCalls []ToolCall
		raw          json.RawMessage
	)
	for {
		chunk, ok := sr.Next()
		if !ok {
			break
		}
		if chunk.Done {
			gotToolCalls = chunk.ToolCalls
			raw = chunk.RawAssistant
		}
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}

	if len(gotToolCalls) != 1 || gotToolCalls[0].ID != "call_abc" {
		t.Fatalf("expected 1 tool call with id call_abc, got %+v", gotToolCalls)
	}
	if len(raw) == 0 {
		t.Fatal("final chunk produced empty RawAssistant")
	}

	// RawAssistant must include the tool_calls block. Parse it
	// loosely (don't pin field order) — we only need the IDs to
	// survive serialization so the next turn replays them.
	var parsed struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("RawAssistant is not valid JSON: %v\n---\n%s", err, string(raw))
	}
	if parsed.Role != "assistant" {
		t.Errorf("RawAssistant.role = %q, want \"assistant\"", parsed.Role)
	}
	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("RawAssistant.tool_calls len = %d, want 1\n---\n%s",
			len(parsed.ToolCalls), string(raw))
	}
	if parsed.ToolCalls[0].ID != "call_abc" {
		t.Errorf("RawAssistant.tool_calls[0].id = %q, want call_abc",
			parsed.ToolCalls[0].ID)
	}
	if parsed.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("RawAssistant.tool_calls[0].function.name = %q, want read_file",
			parsed.ToolCalls[0].Function.Name)
	}

	// And the end-to-end invariant that closes the loop on the
	// reported 400: when we feed this assistant message back through
	// toAPIMessages alongside a matching tool reply, findOrphanToolCalls
	// must NOT flag it as orphan. If it does, the wire payload would
	// strip the tool reply and OpenAI would reject the request.
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: gotToolCalls, RawAssistant: raw},
		{Role: "tool", ToolCallID: "call_abc", Content: "file contents"},
	}
	orphanAssist, orphanTool := findOrphanToolCalls(msgs)
	if orphanAssist[1] {
		t.Error("assistant flagged as orphan despite matching tool reply")
	}
	if orphanTool[2] {
		t.Error("tool reply flagged as orphan despite matching assistant.tool_calls")
	}

	// Also double-check the wire output doesn't drop the assistant's
	// tool_calls — toAPIMessages preferring RawAssistant must give a
	// payload that still carries "tool_calls".
	wire := toAPIMessages(msgs)
	if len(wire) != 3 {
		t.Fatalf("toAPIMessages returned %d msgs, want 3", len(wire))
	}
	if !strings.Contains(string(wire[1]), `"tool_calls"`) {
		t.Errorf("wire-format assistant message has no tool_calls:\n%s", string(wire[1]))
	}
}

func TestToAPIMessagesRebuildAssistantKeepsReasoningContent(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{
			Role:      "assistant",
			Content:   "",
			Thinking:  "need to browse before answering",
			ToolCalls: []ToolCall{{ID: "call_abc", Type: "function", Function: FunctionCall{Name: "web_fetch", Arguments: `{"url":"https://example.com"}`}}},
		},
		{Role: "tool", ToolCallID: "call_abc", Content: "ok"},
	}

	wire := toAPIMessages(msgs)
	if len(wire) != 3 {
		t.Fatalf("toAPIMessages returned %d msgs, want 3", len(wire))
	}
	if !strings.Contains(string(wire[1]), `"reasoning_content":"need to browse before answering"`) {
		t.Fatalf("rebuilt assistant message missing reasoning_content:\n%s", string(wire[1]))
	}
	if !strings.Contains(string(wire[1]), `"tool_calls"`) {
		t.Fatalf("rebuilt assistant message missing tool_calls:\n%s", string(wire[1]))
	}
}

// TestToAPIMessagesRejectsNonOpenAIRawAssistant pins the 422 seen after
// switching a session's default provider from an anthropic-compat route
// to DeepSeek's openai-chat route. The Anthropic provider stores its
// thinking replay payload — a bare {type:"thinking",...} content block —
// on Message.RawAssistant with NO "role" field. toAPIMessages used to
// append any non-empty RawAssistant verbatim, so the OpenAI wire carried:
//
//	[..., {"type":"thinking","thinking":"..."} , ...]
//
// and DeepSeek's strict deserializer rejected the request with
// `messages[2]: missing field role`. The raw JSON without a role must be
// rebuilt from the parsed fields instead (Thinking → reasoning_content),
// while genuine OpenAI-format raw JSON is still reused verbatim.
func TestToAPIMessagesRejectsNonOpenAIRawAssistant(t *testing.T) {
	anthropicThinkingRaw := json.RawMessage(
		`{"type":"thinking","thinking":"need to check the file first","signature":"sig123"}`)

	msgs := []Message{
		{Role: "user", Content: "hi"},
		{
			Role:         "assistant",
			Content:      "",
			Thinking:     "need to check the file first",
			ToolCalls:    []ToolCall{{ID: "call_abc", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"IDENTITY.md"}`}}},
			RawAssistant: anthropicThinkingRaw,
		},
		{Role: "tool", ToolCallID: "call_abc", Content: "file contents"},
	}

	wire := toAPIMessages(msgs)
	if len(wire) != 3 {
		t.Fatalf("toAPIMessages returned %d msgs, want 3", len(wire))
	}

	// The thinking block must have been rebuilt, not shipped verbatim: the
	// wire message carries the assistant role and the stored Thinking as
	// reasoning_content (a verbatim thinking block would fail both).
	var am apiMessage
	if err := json.Unmarshal(wire[1], &am); err != nil {
		t.Fatalf("wire[1] is not a valid apiMessage: %v\n---\n%s", err, string(wire[1]))
	}
	if am.Role != "assistant" {
		t.Errorf("rebuilt wire[1].role = %q, want \"assistant\"\n---\n%s", am.Role, string(wire[1]))
	}
	if am.ReasoningContent != "need to check the file first" {
		t.Errorf("rebuilt wire[1].reasoning_content = %q, want the stored Thinking\n---\n%s",
			am.ReasoningContent, string(wire[1]))
	}

	// A genuine OpenAI-format raw message must still be reused verbatim
	// so the byte-identical-prefix cache-hit guarantee survives.
	openAIRaw := json.RawMessage(`{"role":"assistant","content":"hello"}`)
	verbatimMsgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", RawAssistant: openAIRaw},
	}
	verbatimWire := toAPIMessages(verbatimMsgs)
	if string(verbatimWire[1]) != string(openAIRaw) {
		t.Errorf("OpenAI-format RawAssistant must round-trip verbatim:\ngot  %s\nwant %s",
			string(verbatimWire[1]), string(openAIRaw))
	}
}

// Avoid an unused import on io when go vet is strict; keep one
// reference so future edits don't break compile if io is needed.
var _ = io.EOF
