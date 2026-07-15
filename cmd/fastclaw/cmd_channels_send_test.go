package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/spf13/cobra"
)

type recordingChannelSender struct {
	message bus.OutboundMessage
}

func (s *recordingChannelSender) SendMessage(message bus.OutboundMessage) error {
	s.message = message
	return nil
}

func TestChannelsSendCommandRequiresExplicitTargetAndNoArguments(t *testing.T) {
	cmd := channelsCmd()
	send, _, err := cmd.Find([]string{"send"})
	if err != nil {
		t.Fatalf("find send command: %v", err)
	}
	if send == cmd || send.Use != "send" {
		t.Fatalf("send command Use = %q", send.Use)
	}
	if err := send.Args(send, []string{"positional message"}); err == nil {
		t.Fatal("expected positional arguments to be rejected")
	}
	for _, name := range []string{"type", "agent", "chat"} {
		flag := send.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("missing --%s flag", name)
		}
		if flag.Annotations[cobra.BashCompOneRequiredFlag] == nil {
			t.Fatalf("--%s is not marked required", name)
		}
	}
}

func TestChannelsSendDryRunValidatesAndResolvesWithoutCreatingSender(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	agent := store.AgentRecord{ID: "agt_market", UserID: "u_owner", Name: "A-Share Strategist"}
	if err := st.SaveAgent(ctx, &agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := st.SaveChannel(ctx, &store.ChannelRecord{
		ID: "ch_ding", Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
		AccountID: "ding-client", BotToken: "do-not-print", Enabled: true,
	}); err != nil {
		t.Fatalf("save channel: %v", err)
	}
	mediaPath := filepath.Join(t.TempDir(), "trend.png")
	if err := os.WriteFile(mediaPath, []byte("\x89PNG\r\n\x1a\nimage-data"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	cmd := channelsSendCmdWithDeps(channelSendDeps{
		OpenStore: func() (store.Store, error) { return st, nil },
		NewDingTalkSender: func(channelSendTarget, channels.ChannelReplyEndpointStore) (channelSendMessageSender, error) {
			return nil, errors.New("sender must not be created during dry-run")
		},
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--type", "dingtalk", "--agent", agent.Name, "--chat-id", "user:staff-1",
		"--message", "trend", "--media", mediaPath, "--dry-run", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output %q: %v", stdout.String(), err)
	}
	if result["status"] != "dry-run" || result["type"] != "dingtalk" || result["agent_id"] != agent.ID || result["account"] != "ding-client" || result["chat"] != "user:staff-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Contains(stdout.String(), "do-not-print") || strings.Contains(stdout.String(), "mediaId") {
		t.Fatalf("output exposes private delivery data: %s", stdout.String())
	}
}

func TestChannelsSendDeliversTextAndImageAsOneOutboundMessage(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	agent := store.AgentRecord{ID: "agt_market", UserID: "u_owner", Name: "A-Share Strategist"}
	if err := st.SaveAgent(ctx, &agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := st.SaveChannel(ctx, &store.ChannelRecord{
		ID: "ch_ding", Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
		AccountID: "ding-client", BotToken: "ding-secret", Enabled: true,
	}); err != nil {
		t.Fatalf("save channel: %v", err)
	}
	mediaPath := filepath.Join(t.TempDir(), "trend.png")
	if err := os.WriteFile(mediaPath, []byte("\x89PNG\r\n\x1a\nimage-data"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	recorder := &recordingChannelSender{}
	cmd := channelsSendCmdWithDeps(channelSendDeps{
		OpenStore: func() (store.Store, error) { return st, nil },
		NewDingTalkSender: func(target channelSendTarget, _ channels.ChannelReplyEndpointStore) (channelSendMessageSender, error) {
			if target.ClientSecret != "ding-secret" {
				t.Fatalf("sender target = %#v", target)
			}
			return recorder, nil
		},
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--type", "dingtalk", "--agent", agent.Name, "--chat", "group:conversation-1",
		"--message", "trend", "--media", mediaPath, "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if recorder.message.Text != "trend" || recorder.message.ChatID != "group:conversation-1" || recorder.message.AgentID != agent.ID || len(recorder.message.MediaItems) != 1 || recorder.message.MediaItems[0].Filename != "trend.png" {
		t.Fatalf("outbound message = %#v", recorder.message)
	}
	if !strings.Contains(stdout.String(), `"status":"sent"`) || strings.Contains(stdout.String(), "ding-secret") || strings.Contains(stdout.String(), "mediaId") {
		t.Fatalf("normal JSON output exposes private delivery data: %s", stdout.String())
	}
}

func TestPreflightChannelSendMediaLoadsSupportedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trend.png")
	want := []byte("\x89PNG\r\n\x1a\nimage-data")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	item, err := preflightChannelSendMedia(path)
	if err != nil {
		t.Fatalf("preflight media: %v", err)
	}
	if item.Filename != "trend.png" || item.ContentType != "image/png" || string(item.Bytes) != string(want) {
		t.Fatalf("unexpected media item: %#v", item)
	}
}

func TestPreflightChannelSendMediaSupportsDocumentedFormats(t *testing.T) {
	tests := []struct {
		name, contentType string
		data              []byte
	}{
		{"chart", "image/png", []byte("\x89PNG\r\n\x1a\ndata")},
		{"photo.jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0x00}},
		{"chart.gif", "image/gif", []byte("GIF89a-data")},
		{"chart.webp", "image/webp", []byte("RIFFxxxxWEBPdata")},
		{"chart.bmp", "image/bmp", []byte("BM-data")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.name)
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatalf("write media: %v", err)
			}
			item, err := preflightChannelSendMedia(path)
			if err != nil {
				t.Fatalf("preflight media: %v", err)
			}
			if item.ContentType != tt.contentType {
				t.Fatalf("content type = %q, want %q", item.ContentType, tt.contentType)
			}
		})
	}
}

func TestPreflightChannelSendMediaRejectsNonFileAndOversize(t *testing.T) {
	if _, err := preflightChannelSendMedia(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "large.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create media: %v", err)
	}
	if err := file.Truncate(channels.DingTalkMediaMaxBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate media: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close media: %v", err)
	}
	if _, err := preflightChannelSendMedia(path); err == nil || !strings.Contains(err.Error(), "25 MB") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestChannelsSendRejectsInvalidMediaBeforeOpeningStore(t *testing.T) {
	opened := false
	cmd := channelsSendCmdWithDeps(channelSendDeps{
		OpenStore: func() (store.Store, error) {
			opened = true
			return nil, errors.New("store should not open")
		},
		NewDingTalkSender: func(channelSendTarget, channels.ChannelReplyEndpointStore) (channelSendMessageSender, error) {
			return nil, errors.New("sender should not be created")
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"--type", "dingtalk", "--agent", "default", "--chat", "user:staff-1",
		"--media", filepath.Join(t.TempDir(), "missing.png"), "--dry-run",
	})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "open media") {
		t.Fatalf("execute error = %v", err)
	}
	if opened {
		t.Fatal("store opened before local media validation completed")
	}
	if !strings.Contains(stderr.String(), "open media") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestResolveChannelSendTargetUsesOnlyEnabledDingTalkBinding(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	agent := store.AgentRecord{ID: "agt_market", UserID: "u_owner", Name: "A-Share Strategist"}
	if err := st.SaveAgent(ctx, &agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := st.SaveChannel(ctx, &store.ChannelRecord{
		ID: "ch_disabled", Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
		AccountID: "disabled-client", BotToken: "disabled-secret", Enabled: false,
	}); err != nil {
		t.Fatalf("save disabled channel: %v", err)
	}
	if err := st.SaveChannel(ctx, &store.ChannelRecord{
		ID: "ch_enabled", Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
		AccountID: "ding-client", BotToken: "ding-secret", Enabled: true,
	}); err != nil {
		t.Fatalf("save enabled channel: %v", err)
	}

	target, err := resolveChannelSendTarget(ctx, st, channelSendOptions{
		ChannelType: "dingtalk", AgentRef: agent.Name, ChatID: "user:staff-1",
	})
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if target.Agent.ID != agent.ID || target.AccountID != "ding-client" || target.ClientSecret != "ding-secret" || target.ChatID != "user:staff-1" {
		t.Fatalf("unexpected target: %#v", target)
	}
	if err := st.SaveChannel(ctx, &store.ChannelRecord{
		ID: "ch_second", Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
		AccountID: "ding-second", BotToken: "second-secret", Enabled: true,
	}); err != nil {
		t.Fatalf("save second channel: %v", err)
	}
	if _, err := resolveChannelSendTarget(ctx, st, channelSendOptions{
		ChannelType: "dingtalk", AgentRef: agent.Name, ChatID: "user:staff-1",
	}); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("ambiguous target error = %v", err)
	}
	target, err = resolveChannelSendTarget(ctx, st, channelSendOptions{
		ChannelType: "dingtalk", AgentRef: agent.Name, AccountID: "ding-second", ChatID: "user:staff-1",
	})
	if err != nil {
		t.Fatalf("resolve explicit account: %v", err)
	}
	if target.AccountID != "ding-second" || target.ClientSecret != "second-secret" {
		t.Fatalf("explicit target = %#v", target)
	}
}

func TestChannelsSendAccountIDAliasSelectsAmbiguousBinding(t *testing.T) {
	ctx := context.Background()
	st := freshWechatStore(t)
	agent := store.AgentRecord{ID: "agt_market", UserID: "u_owner", Name: "A-Share Strategist"}
	if err := st.SaveAgent(ctx, &agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	for _, account := range []string{"ding-first", "ding-second"} {
		if err := st.SaveChannel(ctx, &store.ChannelRecord{
			ID: "ch_" + account, Type: "dingtalk", UserID: agent.UserID, AgentID: agent.ID,
			AccountID: account, BotToken: account + "-secret", Enabled: true,
		}); err != nil {
			t.Fatalf("save channel: %v", err)
		}
	}
	cmd := channelsSendCmdWithDeps(channelSendDeps{
		OpenStore: func() (store.Store, error) { return st, nil },
		NewDingTalkSender: func(channelSendTarget, channels.ChannelReplyEndpointStore) (channelSendMessageSender, error) {
			return nil, errors.New("sender must not be created during dry-run")
		},
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--type", "dingtalk", "--agent", agent.Name, "--account-id", "ding-second",
		"--chat", "user:staff-1", "--message", "trend", "--dry-run", "--json",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), `"account":"ding-second"`) {
		t.Fatalf("output = %s", stdout.String())
	}
}
