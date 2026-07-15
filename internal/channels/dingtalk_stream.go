package channels

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingstream "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	dinglogger "github.com/open-dingtalk/dingtalk-stream-sdk-go/logger"
)

type dingTalkStreamClient interface {
	RegisterChatBotCallbackRouter(chatbot.IChatBotMessageHandler)
	Start(context.Context) error
	Close()
}

func (d *DingTalk) newStreamClient() dingTalkStreamClient {
	return dingstream.NewStreamClient(
		dingstream.WithAppCredential(dingstream.NewAppCredentialConfig(d.clientID, d.clientSecret)),
		// The SDK's own reconnect loop uses context.Background and cannot be
		// canceled safely. The supervisor below owns reconnects instead.
		dingstream.WithAutoReconnect(false),
	)
}

// Start supervises a sequence of SDK clients. The upstream SDK does not expose
// a connection-done channel, so a process-wide SDK logger observes only its
// fixed disconnect log categories and wakes every DingTalk supervisor. The
// logger cannot identify the source account, so the hub coalesces a real fault
// into one coordinated reconnect and suppresses signals from deliberate Close
// calls. It never inspects or forwards secret-bearing log arguments.
func (d *DingTalk) Start(ctx context.Context) error {
	disconnected, unsubscribe := dingTalkDisconnects.subscribe()
	defer unsubscribe()
	backoff := d.streamReconnectBase
	for {
		for len(disconnected) > 0 {
			<-disconnected
		}
		cli := d.streamClientFactory()
		cli.RegisterChatBotCallbackRouter(func(callbackCtx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
			return nil, d.handleCallback(callbackCtx, data)
		})
		if err := cli.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// SDK connection errors may carry request details. Keep logs
			// categorical and retry with bounded exponential backoff.
			slog.Warn("DingTalk Stream connect failed; retrying", "account", d.accountID, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		connectedAt := time.Now()
		select {
		case <-ctx.Done():
			dingTalkDisconnects.closeIntentionally(cli)
			return nil
		case <-disconnected:
			slog.Warn("DingTalk Stream disconnected; reconnecting", "account", d.accountID)
			dingTalkDisconnects.closeIntentionally(cli)
			if time.Since(connectedAt) >= d.streamStableAfter {
				backoff = d.streamReconnectBase
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

type dingTalkDisconnectHub struct {
	once sync.Once
	mu   sync.Mutex
	subs map[chan struct{}]struct{}

	suppressUntil time.Time
	lastBroadcast time.Time
}

var dingTalkDisconnects dingTalkDisconnectHub

func (h *dingTalkDisconnectHub) subscribe() (<-chan struct{}, func()) {
	h.once.Do(func() {
		h.subs = make(map[chan struct{}]struct{})
		dinglogger.SetLogger(dingTalkSDKLogger{hub: h})
	})
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *dingTalkDisconnectHub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if now.Before(h.suppressUntil) || now.Sub(h.lastBroadcast) < time.Second {
		return
	}
	h.lastBroadcast = now
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// closeIntentionally suppresses the SDK read-error log produced by Close.
// The logger is process-wide and cannot identify which account emitted a log,
// so this prevents a deliberate unregister from cascading into reconnects for
// unrelated accounts. A real disconnect is coalesced into one coordinated
// reconnect signal by broadcast's debounce window.
func (h *dingTalkDisconnectHub) closeIntentionally(cli dingTalkStreamClient) {
	h.mu.Lock()
	h.suppressUntil = time.Now().Add(time.Second)
	h.mu.Unlock()
	cli.Close()
}

type dingTalkSDKLogger struct{ hub *dingTalkDisconnectHub }

func (dingTalkSDKLogger) Debugf(string, ...interface{})   {}
func (dingTalkSDKLogger) Infof(string, ...interface{})    {}
func (dingTalkSDKLogger) Warningf(string, ...interface{}) {}
func (dingTalkSDKLogger) Fatalf(string, ...interface{})   {}
func (l dingTalkSDKLogger) Errorf(format string, _ ...interface{}) {
	for _, prefix := range []string{
		"connection process panic",
		"connection process connect nil",
		"connection process read message error",
		"connection process is closed",
		"connection write ping message error",
		"ping time out, connection is closing",
	} {
		if strings.HasPrefix(format, prefix) {
			l.hub.broadcast()
			return
		}
	}
}
