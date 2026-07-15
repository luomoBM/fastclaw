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
// logger cannot identify the source account, so each supervisor's one-slot
// signal buffer coalesces repeated logs into a coordinated reconnect. A
// deliberate Close can cause this coordination too; that brief churn is safer
// than suppressing an unrelated real disconnect. Secret-bearing arguments are
// never inspected or forwarded.
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
			cli.Close()
			return nil
		case <-disconnected:
			slog.Warn("DingTalk Stream disconnected; reconnecting", "account", d.accountID)
			cli.Close()
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
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
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
