package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type memoryReplyEndpoints struct {
	values map[string]string
}

func TestDingTalkSendPrefersSessionWebhook(t *testing.T) {
	var sessionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		sessionCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"msgtype":"markdown"`) || !strings.Contains(string(body), "hello") {
			t.Fatalf("session body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	endpoints := &memoryReplyEndpoints{values: map[string]string{
		"dingtalk|ding-client|user:staff-1": server.URL + "/session",
	}}
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), endpoints)
	d.httpClient = server.Client()
	if err := d.Send("user:staff-1", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sessionCalls.Load() != 1 {
		t.Fatalf("session calls = %d", sessionCalls.Load())
	}
}

func TestDingTalkSendFallsBackToTypedProactiveAPI(t *testing.T) {
	var tokenCalls, proactiveCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session":
			http.Error(w, "expired", http.StatusGone)
		case "/v1.0/oauth2/accessToken":
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/robot/groupMessages/send":
			proactiveCalls.Add(1)
			if got := r.Header.Get("x-acs-dingtalk-access-token"); got != "token-1" {
				t.Fatalf("access token header = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["openConversationId"] != "cid-group" || body["robotCode"] != "ding-client" {
				t.Fatalf("proactive body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"processQueryKey":"sent-1"}`))
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	endpoints := &memoryReplyEndpoints{values: map[string]string{
		"dingtalk|ding-client|group:cid-group": server.URL + "/session",
	}}
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), endpoints)
	d.httpClient = server.Client()
	d.apiBase = server.URL
	if err := d.Send("group:cid-group", "fallback"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tokenCalls.Load() != 1 || proactiveCalls.Load() != 1 {
		t.Fatalf("calls token=%d proactive=%d", tokenCalls.Load(), proactiveCalls.Load())
	}
}

func TestDingTalkProactiveMarkdownIsChunked(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/robot/oToMessages/batchSend":
			sends.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len([]rune(body["msgParam"].(string))) > dingtalkMarkdownLimit+100 {
				t.Fatalf("oversized msgParam: %d runes", len([]rune(body["msgParam"].(string))))
			}
			_, _ = w.Write([]byte(`{"processQueryKey":"sent"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	d.httpClient = server.Client()
	d.apiBase = server.URL
	if err := d.Send("user:staff-1", strings.Repeat("文", dingtalkMarkdownLimit+500)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sends.Load() != 2 {
		t.Fatalf("proactive sends = %d, want 2", sends.Load())
	}
}

func TestDingTalkValidateCredentialsRejectsMissingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"InvalidParameter","message":"bad credentials"}`))
	}))
	defer server.Close()
	err := dingtalkValidateCredentials(context.Background(), "ding-client", "bad", server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("validation error = %v", err)
	}
}

func (m *memoryReplyEndpoints) SaveChannelReplyEndpoint(_ context.Context, channel, accountID, target, endpoint string, _ time.Time) error {
	if m.values == nil {
		m.values = make(map[string]string)
	}
	m.values[channel+"|"+accountID+"|"+target] = endpoint
	return nil
}

func (m *memoryReplyEndpoints) GetChannelReplyEndpoint(_ context.Context, channel, accountID, target string) (string, error) {
	v, ok := m.values[channel+"|"+accountID+"|"+target]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func TestParseDingTalkTargetRequiresExplicitType(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		kind, id string
		ok       bool
	}{
		{"user:staff-1", "user", "staff-1", true},
		{"group:cidABC", "group", "cidABC", true},
		{"cidABC", "", "", false},
		{"user:", "", "", false},
		{"room:1", "", "", false},
	} {
		kind, id, err := ParseDingTalkTarget(tc.raw)
		if tc.ok && (err != nil || kind != tc.kind || id != tc.id) {
			t.Errorf("ParseDingTalkTarget(%q) = %q, %q, %v", tc.raw, kind, id, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ParseDingTalkTarget(%q) unexpectedly succeeded", tc.raw)
		}
	}
}

func TestDingTalkInboundDirectMessageUsesTypedTargetAndCachesWebhook(t *testing.T) {
	mb := bus.New()
	endpoints := &memoryReplyEndpoints{}
	d, err := NewDingTalk("ding-client", "secret", "ding-client", mb, endpoints)
	if err != nil {
		t.Fatalf("NewDingTalk: %v", err)
	}
	data := &chatbot.BotCallbackDataModel{
		ConversationType:          "1",
		ConversationId:            "cid-transient",
		MsgId:                     "msg-1",
		Msgtype:                   "text",
		SenderStaffId:             "staff-1",
		SenderId:                  "opaque-1",
		SenderNick:                "Alice",
		ChatbotUserId:             "bot-user",
		SessionWebhook:            "https://example.invalid/session",
		SessionWebhookExpiredTime: time.Now().Add(time.Hour).UnixMilli(),
		Text:                      chatbot.BotCallbackDataTextModel{Content: " hello "},
	}
	if err := d.handleCallback(context.Background(), data); err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	select {
	case got := <-mb.Inbound:
		if got.Channel != "dingtalk" || got.AccountID != "ding-client" || got.ChatID != "user:staff-1" {
			t.Fatalf("route = (%q, %q, %q)", got.Channel, got.AccountID, got.ChatID)
		}
		if got.UserID != "staff-1" || got.Text != "hello" || got.PeerKind != "dm" || got.MessageID != "msg-1" {
			t.Fatalf("inbound = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no inbound message")
	}
	if got := endpoints.values["dingtalk|ding-client|user:staff-1"]; got != data.SessionWebhook {
		t.Fatalf("cached webhook = %q", got)
	}
}

func TestDingTalkGroupRequiresBotMention(t *testing.T) {
	mb := bus.New()
	d, err := NewDingTalk("ding-client", "secret", "ding-client", mb, &memoryReplyEndpoints{})
	if err != nil {
		t.Fatal(err)
	}
	base := chatbot.BotCallbackDataModel{
		ConversationType: "2", ConversationId: "cid-group", MsgId: "msg-group",
		Msgtype: "text", SenderStaffId: "staff-1", SenderNick: "Alice",
		ChatbotUserId: "bot-user", Text: chatbot.BotCallbackDataTextModel{Content: "hi"},
	}
	if err := d.handleCallback(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-mb.Inbound:
		t.Fatalf("non-mentioned group message emitted: %#v", got)
	default:
	}

	base.IsInAtList = true
	base.MsgId = "msg-mentioned"
	if err := d.handleCallback(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-mb.Inbound:
		if got.ChatID != "group:cid-group" || got.PeerKind != "group" {
			t.Fatalf("group route = %#v", got)
		}
		if len(got.Mentions) != 1 || got.Mentions[0] != "bot-user" {
			t.Fatalf("mentions = %#v", got.Mentions)
		}
	case <-time.After(time.Second):
		t.Fatal("mentioned group message not emitted")
	}
}

func TestDingTalkInboundImageDownloadsIntoMediaItem(t *testing.T) {
	imageBytes := []byte("fake-png")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/robot/messageFiles/download":
			if r.Header.Get("x-acs-dingtalk-access-token") != "token-1" {
				t.Fatal("missing token on media exchange")
			}
			_, _ = w.Write([]byte(`{"downloadUrl":"` + "http://" + r.Host + `/blob"}`))
		case "/blob":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBytes)
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	mb := bus.New()
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", mb, &memoryReplyEndpoints{})
	d.httpClient = server.Client()
	d.apiBase = server.URL
	data := &chatbot.BotCallbackDataModel{
		ConversationType: "1", MsgId: "img-1", Msgtype: "picture",
		SenderStaffId: "staff-1", SenderNick: "Alice", ChatbotUserId: "bot-user",
		Content: map[string]any{"downloadCode": "download-1", "fileName": "photo.png"},
	}
	if err := d.handleCallback(context.Background(), data); err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	select {
	case got := <-mb.Inbound:
		if len(got.MediaItems) != 1 || got.MediaItems[0].Filename != "photo.png" || string(got.MediaItems[0].Bytes) != string(imageBytes) {
			t.Fatalf("media = %#v", got.MediaItems)
		}
	case <-time.After(time.Second):
		t.Fatal("no inbound image")
	}
}

func TestDingTalkOutboundImageUploadsThenSendsProactively(t *testing.T) {
	var uploaded, sent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/media/upload":
			if r.URL.Query().Get("access_token") != "token-1" || r.URL.Query().Get("type") != "image" {
				t.Fatalf("upload query = %s", r.URL.RawQuery)
			}
			if err := r.ParseMultipartForm(26 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			file, _, err := r.FormFile("media")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			got, _ := io.ReadAll(file)
			if string(got) != "png-data" {
				t.Fatalf("uploaded bytes = %q", got)
			}
			uploaded.Store(true)
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-1"}`))
		case "/v1.0/robot/oToMessages/batchSend":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["msgKey"] != "sampleImageMsg" || !strings.Contains(body["msgParam"].(string), "media-1") {
				t.Fatalf("media send body = %#v", body)
			}
			sent.Store(true)
			_, _ = w.Write([]byte(`{"processQueryKey":"sent-1"}`))
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	d.httpClient = server.Client()
	d.apiBase = server.URL
	d.oapiBase = server.URL
	err := d.SendMessage(bus.OutboundMessage{
		ChatID:     "user:staff-1",
		MediaItems: []bus.MediaItem{{Filename: "photo.png", ContentType: "image/png", Bytes: []byte("png-data")}},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !uploaded.Load() || !sent.Load() {
		t.Fatalf("uploaded=%v sent=%v", uploaded.Load(), sent.Load())
	}
}
