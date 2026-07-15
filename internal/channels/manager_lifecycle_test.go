package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type lifecycleChannel struct {
	name      string
	accountID string
	started   chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func newLifecycleChannel(name, accountID string) *lifecycleChannel {
	return &lifecycleChannel{
		name: name, accountID: accountID,
		started: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (c *lifecycleChannel) Name() string                          { return c.name }
func (c *lifecycleChannel) AccountID() string                     { return c.accountID }
func (c *lifecycleChannel) BotUsername() string                   { return c.name }
func (c *lifecycleChannel) Send(string, string) error             { return nil }
func (c *lifecycleChannel) SendMessage(bus.OutboundMessage) error { return nil }
func (c *lifecycleChannel) SendTyping(string) error               { return nil }
func (c *lifecycleChannel) Start(ctx context.Context) error {
	c.startOnce.Do(func() { close(c.started) })
	<-ctx.Done()
	c.stopOnce.Do(func() { close(c.stopped) })
	return nil
}

func waitLifecycle(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for channel to %s", what)
	}
}

func TestManagerUnregisterStopsRunningChannel(t *testing.T) {
	mb := bus.New()
	m := NewManager(mb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Start(ctx); close(done) }()

	ch := newLifecycleChannel("dingtalk", "ding-app")
	m.RegisterSingletonAndStart(ch)
	waitLifecycle(t, ch.started, "start")

	m.Unregister("dingtalk", "ding-app")
	waitLifecycle(t, ch.stopped, "stop after unregister")
}

func TestManagerReplacingChannelStopsPreviousInstance(t *testing.T) {
	mb := bus.New()
	m := NewManager(mb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Start(ctx)

	old := newLifecycleChannel("dingtalk", "ding-app")
	m.RegisterSingletonAndStart(old)
	waitLifecycle(t, old.started, "start old instance")

	replacement := newLifecycleChannel("dingtalk", "ding-app")
	m.RegisterSingletonAndStart(replacement)
	waitLifecycle(t, old.stopped, "stop replaced instance")
	waitLifecycle(t, replacement.started, "start replacement instance")
}
