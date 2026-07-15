package setup

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestConnectAgentDingTalkValidatesAndPersists(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	const agentID = "agt_dingtalk"
	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{
		ID: agentID, UserID: owner.ID, Name: "DingTalk agent",
		Config: map[string]any{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	previous := validateDingTalkCredentials
	t.Cleanup(func() { validateDingTalkCredentials = previous })
	var gotID, gotSecret string
	validateDingTalkCredentials = func(_ context.Context, clientID, clientSecret string) error {
		gotID, gotSecret = clientID, clientSecret
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/channels/dingtalk",
		bytes.NewBufferString(`{"clientId":" ding-test ","clientSecret":" secret-test "}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", agentID)
	cookie, err := resolver.IssueSession(ctx, owner.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	s.authMiddleware(s.handleConnectAgentDingTalk)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("connect = %d: %s", rr.Code, rr.Body.String())
	}
	if gotID != "ding-test" || gotSecret != "secret-test" {
		t.Fatalf("validator got clientID=%q secret=%q", gotID, gotSecret)
	}
	rec, err := s.dataStore.LookupChannel(ctx, "dingtalk", "ding-test")
	if err != nil || rec == nil {
		t.Fatalf("lookup channel: rec=%v err=%v", rec, err)
	}
	if rec.AgentID != agentID || rec.UserID != owner.ID || rec.BotToken != "secret-test" || !rec.Enabled {
		t.Fatalf("persisted channel mismatch: %+v", rec)
	}
}
