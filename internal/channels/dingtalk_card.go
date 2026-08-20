package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const defaultDingTalkCardStreamInterval = time.Second

func normalizeDingTalkCardOptions(in DingTalkCardOptions) DingTalkCardOptions {
	in.ReplyMode = strings.ToLower(strings.TrimSpace(in.ReplyMode))
	if in.ReplyMode != "card" {
		in.ReplyMode = "markdown"
	}
	in.CardTemplateID = strings.TrimSpace(in.CardTemplateID)
	if in.CardStreamIntervalMS < 200 {
		in.CardStreamIntervalMS = int(defaultDingTalkCardStreamInterval / time.Millisecond)
	}
	return in
}

// StartReplyStream creates one card before the LLM starts producing tokens.
// If card delivery cannot start, the gateway uses its normal Markdown reply.
func (d *DingTalk) StartReplyStream(ctx context.Context, msg bus.InboundMessage) (ReplyStream, bool) {
	if d.card.ReplyMode != "card" || d.card.CardTemplateID == "" {
		return nil, false
	}
	kind, targetID, err := ParseDingTalkTarget(msg.ChatID)
	if err != nil {
		return nil, false
	}
	outTrackID, err := d.createAndDeliverCard(ctx, kind, targetID)
	if err != nil {
		slog.Warn("DingTalk AI card creation failed; falling back to Markdown", "account", d.accountID, "error", err)
		return nil, false
	}
	return &dingTalkReplyStream{d: d, outTrackID: outTrackID, interval: time.Duration(d.card.CardStreamIntervalMS) * time.Millisecond}, true
}

type dingTalkReplyStream struct {
	d          *DingTalk
	outTrackID string
	interval   time.Duration

	mu        sync.Mutex
	content   string
	failed    bool
	finalized bool
	timer     *time.Timer
	// updateMu serializes the PUT /v1.0/card/streaming calls (flush,
	// Finish, Abort) in program order. Without it a timer flush and a
	// Finish can both be in flight for the same outTrackId with
	// independent random GUIDs — the server has no way to order them,
	// and a partial update applied after the finalize leaves the card
	// showing truncated text with the "generating" indicator stuck on.
	updateMu sync.Mutex
}

func (s *dingTalkReplyStream) WriteDelta(delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.finalized {
		return
	}
	s.content += delta
	if s.timer == nil {
		s.timer = time.AfterFunc(s.interval, s.flush)
	}
}

func (s *dingTalkReplyStream) flush() {
	s.mu.Lock()
	if s.failed || s.finalized {
		s.timer = nil
		s.mu.Unlock()
		return
	}
	content := s.content
	s.timer = nil
	s.mu.Unlock()
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if err := s.d.updateCard(context.Background(), s.outTrackID, content, false); err != nil {
		slog.Warn("DingTalk AI card streaming update failed", "account", s.d.accountID, "error", err)
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
	}
}

func (s *dingTalkReplyStream) Finish(ctx context.Context, final string) bool {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.failed || s.finalized {
		s.mu.Unlock()
		return false
	}
	s.finalized = true
	// A canceled turn (user Stop, task timeout) reaches the gateway's
	// Finish(ctx, "") with the draft already streamed to the card.
	// Blanking it destroys the partial the user was mid-read on —
	// finalize with whatever was streamed.
	if strings.TrimSpace(final) == "" {
		final = s.content
	}
	s.content = final
	s.mu.Unlock()
	// Detach from the caller's cancellation, mirroring flush()'s
	// context.Background(): the gateway hands Finish the per-task ctx,
	// which is already dead exactly when a card turn hits the task
	// timeout. A finalize bound to that ctx strands the card in
	// "generating" state and the Markdown fallback is dropped by the
	// same dead ctx — the user loses the answer in both forms.
	ctx = context.WithoutCancel(ctx)
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if err := s.d.updateCard(ctx, s.outTrackID, final, true); err != nil {
		slog.Warn("DingTalk AI card final update failed; falling back to Markdown", "account", s.d.accountID, "error", err)
		return false
	}
	return true
}

func (s *dingTalkReplyStream) Abort(ctx context.Context) {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.failed || s.finalized {
		s.mu.Unlock()
		return
	}
	s.finalized = true
	content := s.content
	s.mu.Unlock()
	if strings.TrimSpace(content) == "" {
		content = "已停止"
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	_ = s.d.updateCard(ctx, s.outTrackID, content, true)
}

func (d *DingTalk) createAndDeliverCard(ctx context.Context, kind, targetID string) (string, error) {
	outTrackID := "fastclaw_" + randomCardID()
	body := map[string]any{
		"cardTemplateId": d.card.CardTemplateID,
		"outTrackId":     outTrackID,
		"callbackType":   "STREAM",
		"cardData": map[string]any{"cardParamMap": map[string]string{
			// AI Card templates use this flag to occupy the available chat
			// width instead of shrinking their Markdown container to content.
			"config":     `{"autoLayout":true,"enableForward":true}`,
			"content":    "",
			"flowStatus": "2",
		}},
		"imGroupOpenSpaceModel": map[string]bool{
			"supportForward": true,
		},
		"imRobotOpenSpaceModel": map[string]bool{
			"supportForward": true,
		},
		"userIdType": 1,
	}
	if kind == "group" {
		body["openSpaceId"] = "dtv1.card//IM_GROUP." + targetID
		body["imGroupOpenDeliverModel"] = map[string]string{"robotCode": d.clientID}
	} else {
		body["openSpaceId"] = "dtv1.card//IM_ROBOT." + targetID
		body["imRobotOpenDeliverModel"] = map[string]string{"spaceType": "IM_ROBOT", "robotCode": d.clientID}
	}
	status, raw, err := d.cardJSON(ctx, http.MethodPost, "/v1.0/card/instances/createAndDeliver", body, false)
	if err != nil {
		return "", fmt.Errorf("DingTalk card delivery request: %w", err)
	}
	if err := dingTalkCardResponseError("delivery", status, raw); err != nil {
		return "", err
	}
	if err := dingTalkCardDeliveryError(raw); err != nil {
		return "", err
	}
	var result struct {
		OutTrackID string `json:"outTrackId"`
		Result     struct {
			OutTrackID string `json:"outTrackId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &result)
	if strings.TrimSpace(result.Result.OutTrackID) != "" {
		return result.Result.OutTrackID, nil
	}
	if strings.TrimSpace(result.OutTrackID) != "" {
		return result.OutTrackID, nil
	}
	return outTrackID, nil
}

// dingTalkCardDeliveryError handles the per-recipient status embedded in a
// successful createAndDeliver response. The HTTP request may be accepted even
// when DingTalk could not show the card to the intended user.
func dingTalkCardDeliveryError(raw []byte) error {
	var response struct {
		Result struct {
			DeliverResults []struct {
				Success  *bool  `json:"success"`
				ErrorMsg string `json:"errorMsg"`
			} `json:"deliverResults"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil
	}
	for _, delivery := range response.Result.DeliverResults {
		if delivery.Success != nil && !*delivery.Success {
			return fmt.Errorf("DingTalk card delivery rejected: %q", truncateDingTalkMessage(strings.TrimSpace(delivery.ErrorMsg)))
		}
	}
	return nil
}

func (d *DingTalk) updateCard(ctx context.Context, outTrackID, content string, finalize bool) error {
	body := map[string]any{
		"outTrackId": outTrackID,
		"guid":       randomCardGUID(),
		"key":        "content",
		"content":    FlattenMarkdownTables(strings.TrimSpace(content)),
		"isFull":     true,
		"isFinalize": finalize,
		"isError":    false,
	}
	status, raw, err := d.cardJSON(ctx, http.MethodPut, "/v1.0/card/streaming", body, false)
	if err != nil {
		return fmt.Errorf("DingTalk card update request: %w", err)
	}
	if err := dingTalkCardResponseError("streaming update", status, raw); err != nil {
		return err
	}
	return nil
}

// dingTalkCardResponseError returns only diagnostic metadata from DingTalk's
// response. It deliberately never includes a card's content or request body.
func dingTalkCardResponseError(operation string, status int, raw []byte) error {
	if status >= 200 && status < 300 {
		var success struct {
			Code    json.RawMessage `json:"code"`
			ErrCode json.RawMessage `json:"errcode"`
			Message string          `json:"message"`
			ErrMsg  string          `json:"errmsg"`
		}
		if json.Unmarshal(raw, &success) != nil || (isDingTalkSuccessCode(success.Code) && isDingTalkSuccessCode(success.ErrCode)) {
			return nil
		}
		message := strings.TrimSpace(success.Message)
		if message == "" {
			message = strings.TrimSpace(success.ErrMsg)
		}
		return fmt.Errorf("DingTalk card %s rejected: status=%d code=%s message=%q", operation, status, dingTalkCode(success.Code, success.ErrCode), truncateDingTalkMessage(message))
	}
	var failure struct {
		Code      json.RawMessage `json:"code"`
		ErrCode   json.RawMessage `json:"errcode"`
		Message   string          `json:"message"`
		ErrMsg    string          `json:"errmsg"`
		RequestID string          `json:"requestId"`
	}
	_ = json.Unmarshal(raw, &failure)
	message := strings.TrimSpace(failure.Message)
	if message == "" {
		message = strings.TrimSpace(failure.ErrMsg)
	}
	return fmt.Errorf("DingTalk card %s failed: status=%d code=%s message=%q request_id=%s", operation, status, dingTalkCode(failure.Code, failure.ErrCode), truncateDingTalkMessage(message), strings.TrimSpace(failure.RequestID))
}

func isDingTalkSuccessCode(raw json.RawMessage) bool {
	code := strings.Trim(strings.TrimSpace(string(raw)), "\"")
	return code == "" || code == "0" || strings.EqualFold(code, "ok")
}

func dingTalkCode(code, errCode json.RawMessage) string {
	if !isDingTalkSuccessCode(code) {
		return strings.Trim(strings.TrimSpace(string(code)), "\"")
	}
	if !isDingTalkSuccessCode(errCode) {
		return strings.Trim(strings.TrimSpace(string(errCode)), "\"")
	}
	return "unknown"
}

func truncateDingTalkMessage(message string) string {
	const maxRunes = 300
	runes := []rune(message)
	if len(runes) <= maxRunes {
		return message
	}
	return string(runes[:maxRunes]) + "…"
}

func (d *DingTalk) cardJSON(ctx context.Context, method, path string, payload any, refreshed bool) (int, []byte, error) {
	token, err := d.getAccessToken(ctx, refreshed)
	if err != nil {
		return 0, nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, d.apiBase+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !refreshed {
		return d.cardJSON(ctx, method, path, payload, true)
	}
	return resp.StatusCode, body, nil
}

func randomCardID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func randomCardGUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return randomCardID()
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
