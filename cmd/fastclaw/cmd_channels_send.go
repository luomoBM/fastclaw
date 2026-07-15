package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/spf13/cobra"
)

type channelSendOptions struct {
	ChannelType string
	AgentRef    string
	AccountID   string
	ChatID      string
	Message     string
	MediaPath   string
	DryRun      bool
	JSON        bool
}

type channelSendTarget struct {
	Agent        store.AgentRecord
	AccountID    string
	ClientSecret string
	ChatID       string
}

type channelSendMessageSender interface {
	SendMessage(bus.OutboundMessage) error
}

type channelSendDeps struct {
	OpenStore         func() (store.Store, error)
	NewDingTalkSender func(channelSendTarget, channels.ChannelReplyEndpointStore) (channelSendMessageSender, error)
}

func channelsSendCmd() *cobra.Command {
	return channelsSendCmdWithDeps(channelSendDeps{
		OpenStore: openStoreFromEnv,
		NewDingTalkSender: func(target channelSendTarget, endpoints channels.ChannelReplyEndpointStore) (channelSendMessageSender, error) {
			return channels.NewDingTalkSender(target.AccountID, target.ClientSecret, target.AccountID, endpoints)
		},
	})
}

func channelsSendCmdWithDeps(deps channelSendDeps) *cobra.Command {
	var opts channelSendOptions
	var accountAlias, chatAlias string
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a message through a connected channel",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.AccountID == "" && accountAlias != "" {
				opts.AccountID = accountAlias
			}
			if opts.ChatID == "" && chatAlias != "" {
				opts.ChatID = chatAlias
				return cmd.Flags().Set("chat", chatAlias)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.ChannelType != "dingtalk" {
				return fmt.Errorf("channels send does not support channel type %q", opts.ChannelType)
			}
			opts.Message = strings.TrimSpace(opts.Message)
			if opts.Message == "" && strings.TrimSpace(opts.MediaPath) == "" {
				return errors.New("at least one of --message or --media is required")
			}
			if _, _, err := channels.ParseDingTalkTarget(opts.ChatID); err != nil {
				return err
			}
			var mediaItems []bus.MediaItem
			if strings.TrimSpace(opts.MediaPath) != "" {
				item, err := preflightChannelSendMedia(opts.MediaPath)
				if err != nil {
					return err
				}
				mediaItems = []bus.MediaItem{item}
			}
			st, err := deps.OpenStore()
			if err != nil {
				return err
			}
			defer st.Close()
			target, err := resolveChannelSendTarget(cmd.Context(), st, opts)
			if err != nil {
				return err
			}
			if opts.DryRun {
				return writeChannelSendResult(cmd, "dry-run", opts, *target, mediaItems)
			}
			sender, err := deps.NewDingTalkSender(*target, st)
			if err != nil {
				return err
			}
			if err := sender.SendMessage(bus.OutboundMessage{
				Channel: opts.ChannelType, AccountID: target.AccountID, AgentID: target.Agent.ID,
				ChatID: target.ChatID, Text: opts.Message, MediaItems: mediaItems,
			}); err != nil {
				return err
			}
			return writeChannelSendResult(cmd, "sent", opts, *target, mediaItems)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.ChannelType, "type", "", "channel type (required)")
	flags.StringVar(&opts.AgentRef, "agent", "", "agent name or id (required)")
	flags.StringVar(&opts.AccountID, "account", "", "channel account id (required when ambiguous)")
	flags.StringVar(&accountAlias, "account-id", "", "alias for --account")
	flags.StringVar(&opts.ChatID, "chat", "", "channel destination (required)")
	flags.StringVar(&chatAlias, "chat-id", "", "alias for --chat")
	flags.StringVar(&opts.Message, "message", "", "message text")
	flags.StringVar(&opts.MediaPath, "media", "", "local image path")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "validate and resolve without sending")
	flags.BoolVar(&opts.JSON, "json", false, "print machine-readable output")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("agent")
	_ = cmd.MarkFlagRequired("chat")
	return cmd
}

func writeChannelSendResult(cmd *cobra.Command, status string, opts channelSendOptions, target channelSendTarget, mediaItems []bus.MediaItem) error {
	if opts.JSON {
		result := map[string]any{
			"status": status, "type": opts.ChannelType, "agent": target.Agent.Name,
			"agent_id": target.Agent.ID, "account": target.AccountID, "chat": target.ChatID,
		}
		if len(mediaItems) > 0 {
			result["media"] = map[string]any{
				"filename": mediaItems[0].Filename, "content_type": mediaItems[0].ContentType,
				"size": len(mediaItems[0].Bytes),
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	verb := "sent"
	if status == "dry-run" {
		verb = "would send"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s via %s account %s to %s (agent=%s)\n", verb, opts.ChannelType, target.AccountID, target.ChatID, target.Agent.Name)
	return err
}

func preflightChannelSendMedia(path string) (bus.MediaItem, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return bus.MediaItem{}, errors.New("media path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return bus.MediaItem{}, fmt.Errorf("open media: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return bus.MediaItem{}, fmt.Errorf("stat media: %w", err)
	}
	if !info.Mode().IsRegular() {
		return bus.MediaItem{}, errors.New("media must be a regular file")
	}
	if info.Size() > channels.DingTalkMediaMaxBytes {
		return bus.MediaItem{}, errors.New("media exceeds 25 MB")
	}
	data, err := io.ReadAll(io.LimitReader(file, channels.DingTalkMediaMaxBytes+1))
	if err != nil {
		return bus.MediaItem{}, fmt.Errorf("read media: %w", err)
	}
	if len(data) > channels.DingTalkMediaMaxBytes {
		return bus.MediaItem{}, errors.New("media exceeds 25 MB")
	}
	contentType := channelSendImageContentType(data)
	if contentType == "" {
		return bus.MediaItem{}, errors.New("unsupported media format: expected PNG, JPEG, GIF, WebP, or BMP")
	}
	return bus.MediaItem{Filename: filepath.Base(path), ContentType: contentType, Bytes: data}, nil
}

func channelSendImageContentType(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case len(data) >= 2 && bytes.Equal(data[:2], []byte("BM")):
		return "image/bmp"
	default:
		return ""
	}
}

func resolveChannelSendTarget(ctx context.Context, st store.Store, opts channelSendOptions) (*channelSendTarget, error) {
	agent, err := agentcli.Resolve(ctx, st, opts.AgentRef)
	if err != nil {
		return nil, err
	}
	bindings, err := st.ListChannels(ctx, agent.UserID, agent.ID)
	if err != nil {
		return nil, err
	}
	var candidates []store.ChannelRecord
	for _, binding := range bindings {
		if !binding.Enabled || binding.Type != opts.ChannelType {
			continue
		}
		if opts.AccountID != "" && binding.AccountID != opts.AccountID {
			continue
		}
		candidates = append(candidates, binding)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled %s channel binding found for agent %q", opts.ChannelType, opts.AgentRef)
	}
	if len(candidates) > 1 {
		return nil, errors.New("multiple enabled channel bindings match; pass --account")
	}
	binding := candidates[0]
	return &channelSendTarget{
		Agent:        *agent,
		AccountID:    binding.AccountID,
		ClientSecret: binding.BotToken,
		ChatID:       opts.ChatID,
	}, nil
}
