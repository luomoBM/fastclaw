package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingstream "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const dingtalkReplyEndpointFallbackTTL = time.Hour

const (
	dingtalkDefaultAPIBase = "https://api.dingtalk.com"
	dingtalkMarkdownLimit  = 3800
)

// ChannelReplyEndpointStore is the narrow persistence seam needed by
// temporary platform reply URLs. store.DBStore implements it.
type ChannelReplyEndpointStore interface {
	SaveChannelReplyEndpoint(ctx context.Context, channel, accountID, target, endpoint string, expiresAt time.Time) error
	GetChannelReplyEndpoint(ctx context.Context, channel, accountID, target string) (string, error)
}

// DingTalk implements a DingTalk enterprise robot over Stream mode.
type DingTalk struct {
	clientID     string
	clientSecret string
	accountID    string
	bus          *bus.MessageBus
	endpoints    ChannelReplyEndpointStore
	httpClient   *http.Client
	apiBase      string
	oapiBase     string

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time

	botMu     sync.RWMutex
	botUserID string
}

func NewDingTalk(clientID, clientSecret, accountID string, mb *bus.MessageBus, endpoints ChannelReplyEndpointStore) (*DingTalk, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	accountID = strings.TrimSpace(accountID)
	if clientID == "" || clientSecret == "" || accountID == "" {
		return nil, errors.New("dingtalk: clientID, clientSecret, and accountID are required")
	}
	if mb == nil {
		return nil, errors.New("dingtalk: message bus is required")
	}
	return &DingTalk{
		clientID: clientID, clientSecret: clientSecret, accountID: accountID,
		bus: mb, endpoints: endpoints,
		httpClient: &http.Client{Timeout: 15 * time.Second}, apiBase: dingtalkDefaultAPIBase,
		oapiBase: "https://oapi.dingtalk.com",
	}, nil
}

func DingTalkValidateCredentials(ctx context.Context, clientID, clientSecret string) error {
	return dingtalkValidateCredentials(ctx, clientID, clientSecret, &http.Client{Timeout: 15 * time.Second}, dingtalkDefaultAPIBase)
}

func dingtalkValidateCredentials(ctx context.Context, clientID, clientSecret string, httpClient *http.Client, apiBase string) error {
	raw, err := json.Marshal(map[string]string{
		"appKey": strings.TrimSpace(clientID), "appSecret": strings.TrimSpace(clientSecret),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1.0/oauth2/accessToken", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact DingTalk: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return err
	}
	var result struct {
		AccessToken string `json:"accessToken"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.Unmarshal(body, &result) != nil || result.AccessToken == "" {
		return fmt.Errorf("DingTalk rejected credentials (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func (d *DingTalk) Name() string      { return "dingtalk" }
func (d *DingTalk) AccountID() string { return d.accountID }
func (d *DingTalk) BotUsername() string {
	d.botMu.RLock()
	defer d.botMu.RUnlock()
	return d.botUserID
}

func (d *DingTalk) Start(ctx context.Context) error {
	cli := dingstream.NewStreamClient(
		dingstream.WithAppCredential(dingstream.NewAppCredentialConfig(d.clientID, d.clientSecret)),
		// The SDK's reconnect loop uses context.Background and exposes its
		// control flag without synchronization, so it cannot be stopped safely.
		// Manager-level lifecycle ownership is preferable to a leaked reconnect.
		dingstream.WithAutoReconnect(false),
	)
	cli.RegisterChatBotCallbackRouter(func(callbackCtx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		return nil, d.handleCallback(callbackCtx, data)
	})
	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("dingtalk stream start: %w", err)
	}
	<-ctx.Done()
	cli.Close()
	return nil
}

func (d *DingTalk) Send(chatID, text string) error {
	return d.SendMessage(bus.OutboundMessage{Channel: d.Name(), AccountID: d.accountID, ChatID: chatID, Text: text})
}

func (d *DingTalk) SendMessage(msg bus.OutboundMessage) error {
	kind, targetID, err := ParseDingTalkTarget(msg.ChatID)
	if err != nil {
		return err
	}
	text := FlattenMarkdownTables(strings.TrimSpace(msg.Text))
	if text == "" && len(msg.MediaItems) == 0 {
		return nil
	}
	if endpoint, lookupErr := d.replyEndpoint(msg.ChatID); lookupErr == nil && endpoint != "" {
		sessionText, remaining, prepareErr := d.prepareSessionReply(text, msg.MediaItems)
		if prepareErr == nil && sessionText != "" {
			if err := d.sendSessionMarkdown(endpoint, sessionText); err == nil {
				return d.sendMediaItems(kind, targetID, remaining)
			}
			// sessionWebhook is a secret URL. Transport errors may embed it, so
			// never attach the raw error to structured logs.
			slog.Warn("DingTalk session reply failed; falling back to proactive API", "account", d.accountID, "target", msg.ChatID)
		} else if prepareErr != nil {
			// Upload errors may contain a query-string access token. Keep the
			// diagnostic categorical at this boundary.
			slog.Warn("DingTalk session image preparation failed; falling back to proactive API", "account", d.accountID, "target", msg.ChatID)
		}
	}
	if text != "" {
		if err := d.sendProactiveMarkdown(kind, targetID, text); err != nil {
			return err
		}
	}
	return d.sendMediaItems(kind, targetID, msg.MediaItems)
}

// prepareSessionReply embeds images as DingTalk markdown media references.
// Native files are intentionally left for proactive delivery because the
// session webhook does not support them reliably.
func (d *DingTalk) prepareSessionReply(text string, items []bus.MediaItem) (string, []bus.MediaItem, error) {
	remaining := make([]bus.MediaItem, 0, len(items))
	for _, item := range items {
		if !strings.HasPrefix(item.ContentType, "image/") {
			remaining = append(remaining, item)
			continue
		}
		if len(item.Bytes) > 25*1024*1024 {
			return "", nil, fmt.Errorf("attachment %q exceeds 25 MB", item.Filename)
		}
		mediaID, err := d.uploadMedia(context.Background(), item, "image")
		if err != nil {
			return "", nil, err
		}
		ref := fmt.Sprintf("![%s](%s)", filepath.Base(item.Filename), mediaID)
		if text == "" {
			text = ref
		} else {
			text += "\n\n" + ref
		}
	}
	return text, remaining, nil
}

func (d *DingTalk) SendTyping(string) error { return nil }

func ParseDingTalkTarget(raw string) (kind, id string, err error) {
	raw = strings.TrimSpace(raw)
	kind, id, ok := strings.Cut(raw, ":")
	if !ok || (kind != "user" && kind != "group") || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("invalid DingTalk target %q: expected user:<staffId> or group:<conversationId>", raw)
	}
	return kind, strings.TrimSpace(id), nil
}

func (d *DingTalk) handleCallback(ctx context.Context, data *chatbot.BotCallbackDataModel) error {
	if data == nil {
		return nil
	}
	if data.ChatbotUserId != "" {
		d.botMu.Lock()
		d.botUserID = data.ChatbotUserId
		d.botMu.Unlock()
	}
	if data.SenderId != "" && data.SenderId == data.ChatbotUserId {
		return nil
	}
	peerKind := "dm"
	targetID := strings.TrimSpace(data.SenderStaffId)
	if data.ConversationType != "1" {
		peerKind = "group"
		if !data.IsInAtList {
			return nil
		}
		targetID = strings.TrimSpace(data.ConversationId)
	}
	if targetID == "" {
		return errors.New("dingtalk inbound message has no staff or conversation identifier")
	}
	target := peerKindTarget(peerKind) + ":" + targetID
	if data.SessionWebhook != "" && d.endpoints != nil {
		expiresAt := time.UnixMilli(data.SessionWebhookExpiredTime)
		if data.SessionWebhookExpiredTime <= 0 || !expiresAt.After(time.Now()) {
			expiresAt = time.Now().Add(dingtalkReplyEndpointFallbackTTL)
		}
		if err := d.endpoints.SaveChannelReplyEndpoint(ctx, d.Name(), d.accountID, target, data.SessionWebhook, expiresAt); err != nil {
			slog.Warn("cache DingTalk session webhook failed", "account", d.accountID, "target", target, "error", err)
		}
	}
	if data.Msgtype != "text" {
		if data.Msgtype != "picture" && data.Msgtype != "file" {
			return nil
		}
	}
	text := strings.TrimSpace(data.Text.Content)
	var mediaItems []bus.MediaItem
	if data.Msgtype == "picture" || data.Msgtype == "file" {
		downloadCode, filename := dingtalkContentFields(data.Content)
		item, err := d.downloadInboundMedia(ctx, downloadCode, filename, data.Msgtype)
		if err != nil {
			text = "[附件未能获取：" + err.Error() + "]"
		} else {
			mediaItems = []bus.MediaItem{item}
			text = "请查看我发送的附件。"
		}
	}
	if text == "" && len(mediaItems) == 0 {
		return nil
	}
	mentions := []string(nil)
	if peerKind == "group" && data.IsInAtList && data.ChatbotUserId != "" {
		mentions = []string{data.ChatbotUserId}
	}
	msg := bus.InboundMessage{
		Channel: d.Name(), AccountID: d.accountID, ChatID: target,
		UserID: targetUserID(data), MessageID: data.MsgId, Text: text,
		PeerKind: peerKind, SenderName: data.SenderNick, Mentions: mentions,
		MediaItems: mediaItems,
	}
	select {
	case d.bus.Inbound <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func peerKindTarget(peerKind string) string {
	if peerKind == "group" {
		return "group"
	}
	return "user"
}

func targetUserID(data *chatbot.BotCallbackDataModel) string {
	if id := strings.TrimSpace(data.SenderStaffId); id != "" {
		return id
	}
	return strings.TrimSpace(data.SenderId)
}

func (d *DingTalk) replyEndpoint(target string) (string, error) {
	if d.endpoints == nil {
		return "", errors.New("dingtalk reply endpoint store unavailable")
	}
	return d.endpoints.GetChannelReplyEndpoint(context.Background(), d.Name(), d.accountID, target)
}

func (d *DingTalk) sendSessionMarkdown(endpoint, text string) error {
	chunks := splitDingTalkMarkdown(text, dingtalkMarkdownLimit)
	for i, chunk := range chunks {
		body := map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": sessionChunkTitle(i, len(chunks)),
				"text":  chunk,
			},
		}
		status, response, err := d.postJSON(context.Background(), endpoint, body, "")
		if err != nil {
			return fmt.Errorf("dingtalk session reply: %w", err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("dingtalk session reply: HTTP %d", status)
		}
		var result struct {
			ErrCode int    `json:"errcode"`
			Code    string `json:"code"`
		}
		if len(response) > 0 && json.Unmarshal(response, &result) == nil && (result.ErrCode != 0 || (result.Code != "" && result.Code != "0")) {
			return fmt.Errorf("dingtalk session reply rejected: code %d%s", result.ErrCode, result.Code)
		}
	}
	return nil
}

func sessionChunkTitle(index, total int) string {
	if total <= 1 {
		return "FastClaw"
	}
	return fmt.Sprintf("FastClaw (%d/%d)", index+1, total)
}

func splitDingTalkMarkdown(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	chunks := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		n := min(limit, len(runes))
		if n < len(runes) {
			for i := n; i > n/2; i-- {
				if runes[i-1] == '\n' {
					n = i
					break
				}
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:n])))
		runes = runes[n:]
	}
	return chunks
}

func (d *DingTalk) sendProactiveMarkdown(kind, targetID, text string) error {
	chunks := splitDingTalkMarkdown(text, dingtalkMarkdownLimit)
	for i, chunk := range chunks {
		if err := d.sendProactive(context.Background(), kind, targetID, "sampleMarkdown", map[string]string{
			"title": sessionChunkTitle(i, len(chunks)), "text": chunk,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *DingTalk) sendProactive(ctx context.Context, kind, targetID, msgKey string, msgParam map[string]string) error {
	path := "/v1.0/robot/oToMessages/batchSend"
	payload := map[string]any{
		"robotCode": d.clientID, "msgKey": msgKey, "msgParam": mustJSON(msgParam),
	}
	if kind == "group" {
		path = "/v1.0/robot/groupMessages/send"
		payload["openConversationId"] = targetID
	} else {
		payload["userIds"] = []string{targetID}
	}
	token, err := d.getAccessToken(ctx, false)
	if err != nil {
		return err
	}
	status, _, err := d.postJSON(ctx, d.apiBase+path, payload, token)
	if err != nil {
		return fmt.Errorf("dingtalk proactive send: %w", err)
	}
	if status == http.StatusUnauthorized {
		token, err = d.getAccessToken(ctx, true)
		if err != nil {
			return err
		}
		status, _, err = d.postJSON(ctx, d.apiBase+path, payload, token)
	}
	if err != nil {
		return fmt.Errorf("dingtalk proactive send: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("dingtalk proactive send: HTTP %d", status)
	}
	return nil
}

func (d *DingTalk) getAccessToken(ctx context.Context, force bool) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if !force && d.accessToken != "" && time.Until(d.tokenExpiry) > time.Minute {
		return d.accessToken, nil
	}
	body := map[string]string{"appKey": d.clientID, "appSecret": d.clientSecret}
	status, raw, err := d.postJSON(ctx, d.apiBase+"/v1.0/oauth2/accessToken", body, "")
	if err != nil {
		return "", fmt.Errorf("dingtalk access token: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("dingtalk access token: HTTP %d", status)
	}
	var result struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.AccessToken == "" {
		return "", errors.New("dingtalk access token response missing accessToken")
	}
	if result.ExpireIn <= 0 {
		result.ExpireIn = 7200
	}
	d.accessToken = result.AccessToken
	d.tokenExpiry = time.Now().Add(time.Duration(result.ExpireIn) * time.Second)
	return d.accessToken, nil
}

func (d *DingTalk) postJSON(ctx context.Context, endpoint string, payload any, token string) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	var lastStatus int
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("x-acs-dingtalk-access-token", token)
		}
		resp, err := d.httpClient.Do(req)
		if err != nil {
			if attempt == 2 {
				return 0, nil, err
			}
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return resp.StatusCode, nil, readErr
		}
		lastStatus = resp.StatusCode
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp.StatusCode, body, nil
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}
	return lastStatus, nil, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (d *DingTalk) sendMediaItems(kind, targetID string, items []bus.MediaItem) error {
	for _, item := range items {
		if len(item.Bytes) > 25*1024*1024 {
			return fmt.Errorf("dingtalk attachment %q exceeds 25 MB", item.Filename)
		}
		mediaType := "file"
		if strings.HasPrefix(item.ContentType, "image/") {
			mediaType = "image"
		}
		mediaID, err := d.uploadMedia(context.Background(), item, mediaType)
		if err != nil {
			return err
		}
		if err := d.sendProactiveMedia(context.Background(), kind, targetID, item, mediaType, mediaID); err != nil {
			return err
		}
	}
	return nil
}

func (d *DingTalk) uploadMedia(ctx context.Context, item bus.MediaItem, mediaType string) (string, error) {
	token, err := d.getAccessToken(ctx, false)
	if err != nil {
		return "", err
	}
	mediaID, authRejected, err := d.uploadMediaWithToken(ctx, item, mediaType, token)
	if !authRejected {
		return mediaID, err
	}
	token, err = d.getAccessToken(ctx, true)
	if err != nil {
		return "", err
	}
	mediaID, _, err = d.uploadMediaWithToken(ctx, item, mediaType, token)
	return mediaID, err
}

func (d *DingTalk) uploadMediaWithToken(ctx context.Context, item bus.MediaItem, mediaType, token string) (string, bool, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("media", filepath.Base(item.Filename))
	if err != nil {
		return "", false, err
	}
	if _, err := part.Write(item.Bytes); err != nil {
		return "", false, err
	}
	if err := w.Close(); err != nil {
		return "", false, err
	}
	values := url.Values{"access_token": {token}, "type": {mediaType}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.oapiBase+"/media/upload?"+values.Encode(), &body)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := d.httpClient.Do(req)
	if err != nil {
		// The request URL contains the access token, so never wrap the
		// transport error (net/http includes the URL in its text).
		return "", false, errors.New("dingtalk media upload request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode == http.StatusUnauthorized, fmt.Errorf("dingtalk media upload: HTTP %d", resp.StatusCode)
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		MediaID string `json:"media_id"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", false, errors.New("dingtalk media upload response invalid")
	}
	authRejected := result.ErrCode == 40014 || result.ErrCode == 42001
	if result.ErrCode != 0 || result.MediaID == "" {
		return "", authRejected, errors.New("dingtalk media upload rejected")
	}
	return result.MediaID, false, nil
}

func (d *DingTalk) sendProactiveMedia(ctx context.Context, kind, targetID string, item bus.MediaItem, mediaType, mediaID string) error {
	msgKey := "sampleFile"
	msgParam := map[string]string{
		"mediaId": mediaID, "fileName": filepath.Base(item.Filename),
		"fileType": strings.TrimPrefix(filepath.Ext(item.Filename), "."),
	}
	if mediaType == "image" {
		msgKey = "sampleImageMsg"
		msgParam = map[string]string{"photoURL": mediaID}
	}
	return d.sendProactive(ctx, kind, targetID, msgKey, msgParam)
}

func dingtalkContentFields(content any) (downloadCode, filename string) {
	raw, err := json.Marshal(content)
	if err != nil {
		return "", ""
	}
	var fields struct {
		DownloadCode string `json:"downloadCode"`
		FileName     string `json:"fileName"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return "", ""
	}
	return strings.TrimSpace(fields.DownloadCode), filepath.Base(strings.TrimSpace(fields.FileName))
}

func (d *DingTalk) downloadInboundMedia(ctx context.Context, downloadCode, filename, messageType string) (bus.MediaItem, error) {
	if downloadCode == "" {
		return bus.MediaItem{}, errors.New("缺少下载凭证")
	}
	token, err := d.getAccessToken(ctx, false)
	if err != nil {
		return bus.MediaItem{}, errors.New("下载鉴权失败")
	}
	status, raw, err := d.postJSON(ctx, d.apiBase+"/v1.0/robot/messageFiles/download", map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    d.clientID,
	}, token)
	if err != nil || status < 200 || status >= 300 {
		return bus.MediaItem{}, errors.New("下载地址获取失败")
	}
	var result struct {
		DownloadURL string `json:"downloadUrl"`
		Data        struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return bus.MediaItem{}, errors.New("下载地址响应无效")
	}
	downloadURL := result.DownloadURL
	if downloadURL == "" {
		downloadURL = result.Data.DownloadURL
	}
	if downloadURL == "" {
		return bus.MediaItem{}, errors.New("下载地址为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return bus.MediaItem{}, errors.New("下载地址无效")
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return bus.MediaItem{}, errors.New("附件下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return bus.MediaItem{}, fmt.Errorf("附件下载失败（HTTP %d）", resp.StatusCode)
	}
	const maxBytes = 25 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return bus.MediaItem{}, errors.New("附件读取失败")
	}
	if len(data) > maxBytes {
		return bus.MediaItem{}, errors.New("附件超过 25 MB")
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if filename == "" || filename == "." {
		filename = messageType
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			filename += exts[0]
		}
	}
	return bus.MediaItem{Filename: filename, ContentType: contentType, Bytes: data}, nil
}
