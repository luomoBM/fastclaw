package main

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func freshWechatStore(t *testing.T) store.Store {
	t.Helper()
	t.Setenv("FASTCLAW_HOME", t.TempDir())
	st, err := store.New(&store.StorageConfig{
		Type:        store.StorageSQLite,
		AutoMigrate: true,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestWechatCmdStructure(t *testing.T) {
	cmd := wechatCmd()
	if cmd.Use != "wechat" {
		t.Fatalf("Use = %q, want wechat", cmd.Use)
	}
	if send := cmd.Commands()[0]; send.Use != "send <message>" {
		t.Fatalf("send Use = %q", send.Use)
	}
}

func TestResolveWeChatSendTargetUsesLatestSession(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	accts, err := users.NewAccounts(st)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	owner, err := accts.Create(ctx, users.CreateInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "password",
		Role:     users.RoleSuperAdmin,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agentID := "agt_default"
	accountID := "bot@im.bot"
	chatID := "openid@im.wechat"
	sessionKey := "s-latest"
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: owner.ID, Name: "default"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		Kind:          store.KindChannel,
		UserID:        owner.ID,
		AgentID:       agentID,
		Name:          "wechat",
		Enabled:       true,
		CredentialKey: accountID,
		Data: map[string]interface{}{
			"accounts": map[string]interface{}{
				accountID: map[string]interface{}{
					"botToken": "token",
					"baseUrl":  "https://ilinkai.weixin.qq.com",
					"userId":   "ilink-user",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save channel: %v", err)
	}
	if err := st.SaveSession(ctx, owner.ID, agentID, sessionKey, &store.SessionRecord{
		Channel:   "wechat",
		AccountID: accountID,
		ChatID:    chatID,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	target, err := resolveWeChatSendTarget(ctx, st, wechatSendOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Agent.ID != agentID || target.AccountID != accountID || target.ChatID != chatID || target.SessionKey != sessionKey {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestResolveWeChatSendTargetRequiresSessionOrChat(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	accts, err := users.NewAccounts(st)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	owner, err := accts.Create(ctx, users.CreateInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "password",
		Role:     users.RoleSuperAdmin,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: "agt_default", UserID: owner.ID, Name: "default"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		Kind:          store.KindChannel,
		UserID:        owner.ID,
		AgentID:       "agt_default",
		Name:          "wechat",
		Enabled:       true,
		CredentialKey: "bot@im.bot",
		Data: map[string]interface{}{
			"accounts": map[string]interface{}{
				"bot@im.bot": map[string]interface{}{"botToken": "token"},
			},
		},
	}); err != nil {
		t.Fatalf("save channel: %v", err)
	}

	if _, err := resolveWeChatSendTarget(ctx, st, wechatSendOptions{}); err == nil {
		t.Fatal("expected missing session error")
	}
}
