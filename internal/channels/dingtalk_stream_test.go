package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type fakeDingTalkStreamClient struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	onClose   func()
}

func newFakeDingTalkStreamClient() *fakeDingTalkStreamClient {
	return &fakeDingTalkStreamClient{started: make(chan struct{}), closed: make(chan struct{})}
}

func (*fakeDingTalkStreamClient) RegisterChatBotCallbackRouter(chatbot.IChatBotMessageHandler) {}
func (c *fakeDingTalkStreamClient) Start(context.Context) error {
	c.startOnce.Do(func() { close(c.started) })
	return nil
}
func (c *fakeDingTalkStreamClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.onClose != nil {
			c.onClose()
		}
	})
}

func TestDingTalkStreamSupervisorReconnectsAndStops(t *testing.T) {
	d, err := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	if err != nil {
		t.Fatal(err)
	}
	created := make(chan *fakeDingTalkStreamClient, 3)
	d.streamClientFactory = func() dingTalkStreamClient {
		client := newFakeDingTalkStreamClient()
		client.onClose = dingTalkDisconnects.broadcast
		created <- client
		return client
	}
	d.streamReconnectBase = time.Millisecond
	d.streamStableAfter = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	first := <-created
	waitLifecycle(t, first.started, "start first DingTalk stream")

	dingTalkDisconnects.broadcast()
	waitLifecycle(t, first.closed, "close disconnected DingTalk stream")
	second := <-created
	waitLifecycle(t, second.started, "start replacement DingTalk stream")

	cancel()
	waitLifecycle(t, second.closed, "close DingTalk stream on cancellation")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DingTalk Start did not stop after cancellation")
	}
}
