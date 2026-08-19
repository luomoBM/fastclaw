package agent

import (
	"crypto/sha256"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func testToolCall(name, args string) provider.ToolCall {
	return provider.ToolCall{Function: provider.FunctionCall{Name: name, Arguments: args}}
}

func TestToolLoopDetectorAllowsProgressingPolling(t *testing.T) {
	var detector toolLoopDetector
	tc := testToolCall("check_status", `{"id":"abc"}`)

	for _, result := range []string{"pending", "processing", "completed"} {
		if detector.Observe(tc, result) {
			t.Fatalf("progressing polling result %q was classified as a loop", result)
		}
	}
}

type argOnlyLoopDetector struct {
	last        argOnlyLoopSignature
	consecutive int
}

type argOnlyLoopSignature struct {
	name      string
	inputHash [32]byte
}

func (d *argOnlyLoopDetector) Observe(tc provider.ToolCall) bool {
	sig := argOnlyLoopSignature{
		name:      tc.Function.Name,
		inputHash: sha256.Sum256([]byte(tc.Function.Arguments)),
	}
	if sig.name == d.last.name && sig.inputHash == d.last.inputHash {
		d.consecutive++
	} else {
		d.consecutive = 1
		d.last = sig
	}
	return d.consecutive >= toolLoopDetectionThreshold
}

func TestToolLoopDetectorAllowsThirdProgressingPollThatArgOnlyDetectorWouldBlock(t *testing.T) {
	tc := testToolCall("check_status", "{\"id\":\"abc\"}")
	results := []string{"pending: queued", "pending: running", "pending: finalizing"}

	var oldDetector argOnlyLoopDetector
	for i := range results {
		got := oldDetector.Observe(tc)
		if i < toolLoopDetectionThreshold-1 && got {
			t.Fatalf("arg-only detector looped before the threshold on poll %d", i+1)
		}
		if i == toolLoopDetectionThreshold-1 && !got {
			t.Fatalf("arg-only detector should block the third identical tool call")
		}
	}

	var detector toolLoopDetector
	for _, result := range results {
		if detector.Observe(tc, result) {
			t.Fatalf("result-aware detector should allow progressing poll result %q", result)
		}
	}
}

func TestToolLoopDetectorDetectsIdenticalInputAndResult(t *testing.T) {
	var detector toolLoopDetector
	tc := testToolCall("check_status", `{"id":"abc"}`)

	for i := 1; i <= toolLoopDetectionThreshold; i++ {
		got := detector.Observe(tc, "pending")
		if i < toolLoopDetectionThreshold && got {
			t.Fatalf("Observe call %d detected a loop before the threshold", i)
		}
		if i == toolLoopDetectionThreshold && !got {
			t.Fatalf("Observe call %d did not detect repeated identical results", i)
		}
	}
}

func TestToolLoopDetectorResetsOnDifferentArguments(t *testing.T) {
	var detector toolLoopDetector
	first := testToolCall("check_status", `{"id":"abc"}`)
	second := testToolCall("check_status", `{"id":"def"}`)

	if detector.Observe(first, "pending") {
		t.Fatal("first observation should not detect a loop")
	}
	if detector.Observe(first, "pending") {
		t.Fatal("second observation should not detect a loop")
	}
	if detector.Observe(second, "pending") {
		t.Fatal("different arguments should reset loop detection")
	}
}