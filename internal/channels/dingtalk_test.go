package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"sync"
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
		var payload struct {
			Markdown struct {
				Title string `json:"title"`
			} `json:"markdown"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Markdown.Title != "hello" {
			t.Fatalf("session title = %q, want message preview", payload.Markdown.Title)
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
			var param map[string]string
			if err := json.Unmarshal([]byte(body["msgParam"].(string)), &param); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(param["title"], "回复预览") || strings.Contains(param["title"], "FastClaw") {
				t.Fatalf("proactive title = %q, want message preview", param["title"])
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
	if err := d.Send("user:staff-1", "# 回复预览\n"+strings.Repeat("文", dingtalkMarkdownLimit+500)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sends.Load() != 2 {
		t.Fatalf("proactive sends = %d, want 2", sends.Load())
	}
}

func TestDingTalkMarkdownTitleUsesFirstMeaningfulLine(t *testing.T) {
	if got := dingtalkMarkdownTitle("\n## **市场摘要**\n后续内容", 0, 1); got != "市场摘要" {
		t.Fatalf("title = %q, want market summary", got)
	}
	if got := dingtalkMarkdownTitle("\n\n", 1, 2); got != "FastClaw (2/2)" {
		t.Fatalf("empty title = %q", got)
	}
}

func TestDingTalkCardStreamCreatesAndFinalizesOneCard(t *testing.T) {
	var createBody, streamBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/card/instances/createAndDeliver":
			if r.Method != http.MethodPost {
				t.Fatalf("create method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"outTrackId":"card-1"}`))
		case "/v1.0/card/streaming":
			if r.Method != http.MethodPut {
				t.Fatalf("stream method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&streamBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card", CardTemplateID: "template.schema", CardStreamIntervalMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	d.httpClient, d.apiBase = server.Client(), server.URL
	stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{Channel: "dingtalk", AccountID: "ding-client", ChatID: "user:staff-1"})
	if !ok {
		t.Fatal("card stream was not started")
	}
	stream.WriteDelta("hello ")
	stream.WriteDelta("world")
	if !stream.Finish(context.Background(), "# hello\nworld") {
		t.Fatal("card stream did not finish")
	}
	if createBody["cardTemplateId"] != "template.schema" || createBody["openSpaceId"] != "dtv1.card//IM_ROBOT.staff-1" {
		t.Fatalf("create body = %#v", createBody)
	}
	params := createBody["cardData"].(map[string]any)["cardParamMap"].(map[string]any)
	if params["config"] != `{"autoLayout":true,"enableForward":true}` {
		t.Fatalf("card layout config = %#v", params["config"])
	}
	if streamBody["outTrackId"] != "card-1" || streamBody["content"] != "# hello\nworld" || streamBody["isFinalize"] != true {
		t.Fatalf("stream body = %#v", streamBody)
	}
	if _, ok := streamBody["guid"].(string); !ok || streamBody["guid"] == "" || streamBody["isError"] != false {
		t.Fatalf("streaming protocol fields = %#v", streamBody)
	}
}

func TestDingTalkCardStreamDisabledWithoutTemplate(t *testing.T) {
	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card"})
	if err != nil {
		t.Fatal(err)
	}
	if stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{ChatID: "user:staff-1"}); ok || stream != nil {
		t.Fatal("card stream should require a template")
	}
}

func TestDingTalkCardStreamFallsBackWhenDeliveryIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/card/instances/createAndDeliver":
			// DingTalk can report a per-recipient delivery failure in an
			// otherwise-successful HTTP response.
			_, _ = w.Write([]byte(`{"result":{"deliverResults":[{"success":false,"errorMsg":"recipient unavailable"}]}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card", CardTemplateID: "template.schema"})
	if err != nil {
		t.Fatal(err)
	}
	d.httpClient, d.apiBase = server.Client(), server.URL
	if stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{ChatID: "user:staff-1"}); ok || stream != nil {
		t.Fatal("card stream should fall back when DingTalk rejects delivery")
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

func TestDingTalkDirectMessageRequiresStaffIDForTypedTarget(t *testing.T) {
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	err := d.handleCallback(context.Background(), &chatbot.BotCallbackDataModel{
		ConversationType: "1", MsgId: "msg-opaque", Msgtype: "text",
		SenderId: "opaque-sender", Text: chatbot.BotCallbackDataTextModel{Content: "hello"},
	})
	if err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("missing staff ID error = %v", err)
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
			var param map[string]string
			_ = json.Unmarshal([]byte(body["msgParam"].(string)), &param)
			if body["msgKey"] != "sampleMarkdown" || !strings.Contains(param["text"], `![photo.png](media-1)`) || param["title"] != "photo.png" {
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

func TestDingTalkOutboundImagePrefersSessionMarkdown(t *testing.T) {
	var sessionBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/media/upload":
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-1"}`))
		case "/session":
			raw, _ := io.ReadAll(r.Body)
			sessionBody = string(raw)
			_, _ = w.Write([]byte(`{"errcode":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	endpoints := &memoryReplyEndpoints{values: map[string]string{
		"dingtalk|ding-client|user:staff-1": server.URL + "/session",
	}}
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), endpoints)
	d.httpClient = server.Client()
	d.apiBase, d.oapiBase = server.URL, server.URL
	err := d.SendMessage(bus.OutboundMessage{
		ChatID: "user:staff-1", Text: "caption",
		MediaItems: []bus.MediaItem{{Filename: "photo.png", ContentType: "image/png", Bytes: []byte("png-data")}},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(sessionBody, "caption") || !strings.Contains(sessionBody, `![photo.png](media-1)`) {
		t.Fatalf("session body = %s", sessionBody)
	}
}

func TestDingTalkOutboundImageReusesUploadForProactiveFallback(t *testing.T) {
	var uploadCalls atomic.Int32
	var proactiveParam map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/media/upload":
			uploadCalls.Add(1)
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-1"}`))
		case "/session":
			w.WriteHeader(http.StatusBadRequest)
		case "/v1.0/robot/oToMessages/batchSend":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["msgKey"] != "sampleMarkdown" {
				t.Fatalf("msgKey = %#v", body["msgKey"])
			}
			if err := json.Unmarshal([]byte(body["msgParam"].(string)), &proactiveParam); err != nil {
				t.Fatalf("decode msgParam: %v", err)
			}
			_, _ = w.Write([]byte(`{"processQueryKey":"sent"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	endpoints := &memoryReplyEndpoints{values: map[string]string{
		"dingtalk|ding-client|user:staff-1": server.URL + "/session",
	}}
	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), endpoints)
	d.httpClient = server.Client()
	d.apiBase, d.oapiBase = server.URL, server.URL
	if err := d.SendMessage(bus.OutboundMessage{
		ChatID: "user:staff-1", Text: "caption",
		MediaItems: []bus.MediaItem{{Filename: "photo.png", ContentType: "image/png", Bytes: []byte("png-data")}},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if uploadCalls.Load() != 1 {
		t.Fatalf("upload calls = %d, want 1", uploadCalls.Load())
	}
	if !strings.Contains(proactiveParam["text"], "caption") || !strings.Contains(proactiveParam["text"], `![photo.png](media-1)`) {
		t.Fatalf("proactive markdown = %#v", proactiveParam)
	}
}

func TestDingTalkOutboundLongTextPlacesImageOnlyInFinalChunk(t *testing.T) {
	var sentTexts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/media/upload":
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-1"}`))
		case "/v1.0/robot/oToMessages/batchSend":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			var param map[string]string
			_ = json.Unmarshal([]byte(body["msgParam"].(string)), &param)
			sentTexts = append(sentTexts, param["text"])
			_, _ = w.Write([]byte(`{"processQueryKey":"sent"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	d.httpClient = server.Client()
	d.apiBase, d.oapiBase = server.URL, server.URL
	if err := d.SendMessage(bus.OutboundMessage{
		ChatID: "user:staff-1", Text: strings.Repeat("文", dingtalkMarkdownLimit+100),
		MediaItems: []bus.MediaItem{{Filename: "photo.png", ContentType: "image/png", Bytes: []byte("png-data")}},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(sentTexts) != 2 || strings.Contains(sentTexts[0], "media-1") || !strings.Contains(sentTexts[1], `![photo.png](media-1)`) {
		t.Fatalf("sent chunks = %#v", sentTexts)
	}
}

func TestDingTalkOutboundMediaRefreshesRejectedToken(t *testing.T) {
	var tokenCalls, uploadCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			n := tokenCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"accessToken":"token-%d","expireIn":7200}`, n)
		case "/media/upload":
			n := uploadCalls.Add(1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"errcode":40014}`))
				return
			}
			if r.URL.Query().Get("access_token") != "token-2" {
				t.Fatalf("refreshed upload token = %q", r.URL.Query().Get("access_token"))
			}
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-2"}`))
		case "/v1.0/robot/oToMessages/batchSend":
			if r.Header.Get("x-acs-dingtalk-access-token") != "token-2" {
				t.Fatalf("send token = %q", r.Header.Get("x-acs-dingtalk-access-token"))
			}
			_, _ = w.Write([]byte(`{"processQueryKey":"sent"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, _ := NewDingTalk("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{})
	d.httpClient = server.Client()
	d.apiBase, d.oapiBase = server.URL, server.URL
	err := d.SendMessage(bus.OutboundMessage{
		ChatID:     "user:staff-1",
		MediaItems: []bus.MediaItem{{Filename: "file.txt", ContentType: "text/plain", Bytes: []byte("data")}},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if tokenCalls.Load() != 2 || uploadCalls.Load() != 2 {
		t.Fatalf("token calls=%d upload calls=%d", tokenCalls.Load(), uploadCalls.Load())
	}
}

// TestDingTalkCardFinishSurvivesCanceledTaskCtx — the finalize PUT is
// the single most important update of a card stream: it flips
// flowStatus off "generating" and carries the full text. The gateway
// calls Finish with the per-task ctx, which is already dead exactly
// when a card-mode turn hits the task timeout (production:
// "DingTalk AI card final update failed; falling back to Markdown …
// context deadline exceeded" followed by "outbound enqueue cancelled"
// — the answer lost in BOTH forms). The periodic flush already uses
// context.Background(); the finalize write must not be the least
// protected update on the stream.
func TestDingTalkCardFinishSurvivesCanceledTaskCtx(t *testing.T) {
	var streamBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/card/instances/createAndDeliver":
			_, _ = w.Write([]byte(`{"outTrackId":"card-1"}`))
		case "/v1.0/card/streaming":
			if err := json.NewDecoder(r.Body).Decode(&streamBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card", CardTemplateID: "template.schema", CardStreamIntervalMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	d.httpClient, d.apiBase = server.Client(), server.URL
	stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{Channel: "dingtalk", AccountID: "ding-client", ChatID: "user:staff-1"})
	if !ok {
		t.Fatal("card stream was not started")
	}
	stream.WriteDelta("partial ")

	// The task ctx dies the instant the task timeout fires — the
	// moment Finish is most needed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !stream.Finish(ctx, "# full\nanswer") {
		t.Fatal("finalize died with the canceled task ctx — card sticks in generating state forever")
	}
	if streamBody["content"] != "# full\nanswer" || streamBody["isFinalize"] != true {
		t.Fatalf("stream body = %#v", streamBody)
	}
}

// TestDingTalkCardFinishWaitsForInflightFlush — a timer flush and a
// Finish must never have PUT /v1.0/card/streaming in flight at the
// same time for one card: they carry independent random GUIDs, so the
// server cannot order them, and a partial update applied after the
// finalize leaves the card truncated with the generating indicator
// stuck on. The stream serializes updates in program order: the
// finalize waits for the in-flight flush to complete.
func TestDingTalkCardFinishWaitsForInflightFlush(t *testing.T) {
	flushEntered := make(chan struct{}, 1)
	releaseFlush := make(chan struct{})
	// Leak-safe release: if the test fails (or serialization regresses),
	// server.Close() must not block forever on the parked handler.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFlush) }) }
	defer release()
	var mu sync.Mutex
	bodies := []map[string]any{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/card/instances/createAndDeliver":
			_, _ = w.Write([]byte(`{"outTrackId":"card-1"}`))
		case "/v1.0/card/streaming":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			bodies = append(bodies, body)
			isFinal := body["isFinalize"] == true
			n := len(bodies)
			mu.Unlock()
			if n == 1 {
				// The flush: park mid-request until the test releases it.
				select {
				case flushEntered <- struct{}{}:
				default:
				}
				<-releaseFlush
			}
			_ = isFinal
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card", CardTemplateID: "template.schema", CardStreamIntervalMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	d.httpClient, d.apiBase = server.Client(), server.URL
	stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{Channel: "dingtalk", AccountID: "ding-client", ChatID: "user:staff-1"})
	if !ok {
		t.Fatal("card stream was not started")
	}
	stream.WriteDelta("partial draft")

	select {
	case <-flushEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("flush PUT never started")
	}

	// Finish while the flush PUT is parked mid-request.
	finished := make(chan bool, 1)
	go func() { finished <- stream.Finish(context.Background(), "# final") }()

	// The finalize must NOT reach the server while the flush is parked.
	select {
	case got := <-finished:
		t.Fatalf("Finish returned %v while its PUT was still queued behind the flush — serialization broken", got)
	case <-time.After(150 * time.Millisecond):
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n != 1 {
			t.Fatalf("%d card PUTs reached the server while the flush was still in flight — finalize raced the flush", n)
		}
	}

	release()
	if !<-finished {
		t.Fatal("Finish must succeed once the flush completes")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("card PUTs = %d, want 2 (flush then finalize)", len(bodies))
	}
	if bodies[1]["isFinalize"] != true || bodies[1]["content"] != "# final" {
		t.Fatalf("finalize body = %#v", bodies[1])
	}
}

// TestDingTalkCardFinishEmptyFinalKeepsPartial — a canceled turn
// (user Stop, task timeout) makes HandleMessage return "" and the
// gateway then calls Finish(ctx, ""). Blanking the card destroys the
// partial draft the user was mid-read on; finalize it with what was
// streamed instead.
func TestDingTalkCardFinishEmptyFinalKeepsPartial(t *testing.T) {
	var streamBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"token-1","expireIn":7200}`))
		case "/v1.0/card/instances/createAndDeliver":
			_, _ = w.Write([]byte(`{"outTrackId":"card-1"}`))
		case "/v1.0/card/streaming":
			if err := json.NewDecoder(r.Body).Decode(&streamBody); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d, err := NewDingTalkWithOptions("ding-client", "secret", "ding-client", bus.New(), &memoryReplyEndpoints{}, DingTalkCardOptions{ReplyMode: "card", CardTemplateID: "template.schema", CardStreamIntervalMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	d.httpClient, d.apiBase = server.Client(), server.URL
	stream, ok := d.StartReplyStream(context.Background(), bus.InboundMessage{Channel: "dingtalk", AccountID: "ding-client", ChatID: "user:staff-1"})
	if !ok {
		t.Fatal("card stream was not started")
	}
	stream.WriteDelta("partial answer the user is reading")

	if !stream.Finish(context.Background(), "") {
		t.Fatal("Finish with empty final must still finalize the card")
	}
	if streamBody["content"] != "partial answer the user is reading" {
		t.Fatalf("final card content = %q, want the streamed partial preserved", streamBody["content"])
	}
	if streamBody["isFinalize"] != true {
		t.Fatalf("isFinalize = %v, want true", streamBody["isFinalize"])
	}
}
