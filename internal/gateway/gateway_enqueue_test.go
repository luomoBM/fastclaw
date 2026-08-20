package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// TestEnqueueOutboundDeliversAfterCtxDone — when the task ctx is
// already dead (task timeout expired with generation complete) but the
// outbound bus has room, the reply must still be delivered: it is the
// user's only copy of the answer. The previous inline
// `select { case mb.Outbound <- out; case <-ctx.Done() }` treated the
// two ready cases as a coin flip (production: "outbound enqueue
// cancelled" immediately after "DingTalk AI card final update failed"
// — the answer lost in both card and Markdown form).
//
// Run with -count to expose the pre-fix coin flip; the deliver-if-room
// fallback makes it deterministic.
func TestEnqueueOutboundDeliversAfterCtxDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mb := bus.New()
	out := bus.OutboundMessage{Text: "the answer"}

	if !enqueueOutbound(ctx, mb, out) {
		t.Fatal("deliverable reply dropped on already-done ctx — user loses their only copy")
	}
	select {
	case got := <-mb.Outbound:
		if got.Text != "the answer" {
			t.Fatalf("delivered %q", got.Text)
		}
	default:
		t.Fatal("enqueueOutbound reported success but nothing landed on the bus")
	}
}

// TestEnqueueOutboundDropsOnlyWhenBusFull — the bounded-wait guarantee
// survives: with a dead ctx AND a full bus, the enqueue gives up
// immediately instead of blocking a taskQueue slot.
func TestEnqueueOutboundDropsOnlyWhenBusFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mb := bus.New()
	for i := 0; i < cap(mb.Outbound); i++ {
		mb.Outbound <- bus.OutboundMessage{Text: "filler"}
	}
	if enqueueOutbound(ctx, mb, bus.OutboundMessage{Text: "x"}) {
		t.Fatal("expected drop when bus is full and ctx is done — must not block")
	}
}

// TestEnqueueOutboundDeliversLiveCtx — the ordinary path: live ctx,
// room on the bus, message delivered.
func TestEnqueueOutboundDeliversLiveCtx(t *testing.T) {
	mb := bus.New()
	if !enqueueOutbound(context.Background(), mb, bus.OutboundMessage{Text: "ok"}) {
		t.Fatal("live ctx with room must deliver")
	}
}
