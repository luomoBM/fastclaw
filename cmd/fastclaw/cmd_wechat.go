package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type wechatSendOptions struct {
	AgentRef  string
	AccountID string
	SessionID string
	ChatID    string
	DryRun    bool
}

type wechatSendTarget struct {
	Agent      store.AgentRecord
	UserID     string
	AccountID  string
	BotToken   string
	BaseURL    string
	ILinkUser  string
	ChatID     string
	SessionKey string
	UpdatedAt  time.Time
}

func wechatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wechat",
		Short: "WeChat channel utilities",
	}
	cmd.AddCommand(wechatSendCmd())
	return cmd
}

func wechatSendCmd() *cobra.Command {
	var opts wechatSendOptions
	cmd := &cobra.Command{
		Use:   "send <message>",
		Short: "Send a message to the latest WeChat conversation",
		Long: `Send a plain-text message through a connected WeChat channel.

By default, FastClaw picks the default/only agent, the connected WeChat
account, and the latest WeChat session, so the common form is:

  fastclaw wechat send "hello"

Use --agent, --account-id, --session, or --chat-id only when you need to
override the automatic target selection.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" {
				return errors.New("message is empty")
			}

			st, err := openStoreFromEnv()
			if err != nil {
				return err
			}
			defer st.Close()

			ctx := context.Background()
			target, err := resolveWeChatSendTarget(ctx, st, opts)
			if err != nil {
				return err
			}

			if opts.DryRun {
				fmt.Printf("would send via WeChat account %s to chat %s", target.AccountID, target.ChatID)
				if target.SessionKey != "" {
					fmt.Printf(" (agent=%s session=%s)", target.Agent.Name, target.SessionKey)
				} else {
					fmt.Printf(" (agent=%s)", target.Agent.Name)
				}
				fmt.Println()
				return nil
			}

			wc, err := channels.NewWeChat(target.BotToken, target.BaseURL, target.ILinkUser, target.AccountID, nil)
			if err != nil {
				return err
			}
			if err := wc.Send(target.ChatID, text); err != nil {
				return err
			}

			fmt.Printf("sent via WeChat account %s to chat %s", target.AccountID, target.ChatID)
			if target.SessionKey != "" {
				fmt.Printf(" (agent=%s session=%s)", target.Agent.Name, target.SessionKey)
			} else {
				fmt.Printf(" (agent=%s)", target.Agent.Name)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().StringVarP(&opts.AgentRef, "agent", "a", "", "agent name or id (default: latest/default WeChat-enabled agent)")
	cmd.Flags().StringVar(&opts.AccountID, "account-id", "", "WeChat account id / ilink_bot_id (default: inferred)")
	cmd.Flags().StringVar(&opts.SessionID, "session", "", "FastClaw session key to address (default: latest WeChat session)")
	cmd.Flags().StringVar(&opts.ChatID, "chat-id", "", "raw WeChat chat id / user id (default: inferred from latest session)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "resolve and print the target without sending")
	return cmd
}

func resolveWeChatSendTarget(ctx context.Context, st store.Store, opts wechatSendOptions) (*wechatSendTarget, error) {
	targets, err := collectWeChatSendTargets(ctx, st, opts.AgentRef)
	if err != nil {
		return nil, err
	}
	if opts.AccountID != "" {
		targets = filterWeChatTargets(targets, func(t wechatSendTarget) bool {
			return t.AccountID == opts.AccountID
		})
	}
	if len(targets) == 0 {
		return nil, errors.New("no enabled WeChat channel found for the requested scope")
	}

	if opts.ChatID != "" {
		for i := range targets {
			targets[i].ChatID = opts.ChatID
		}
		return chooseWeChatTarget(targets, opts, "chat-id")
	}

	if opts.SessionID != "" {
		withSession := make([]wechatSendTarget, 0, len(targets))
		for _, t := range targets {
			filled, ok, err := attachWeChatSessionByKey(ctx, st, t, opts.SessionID)
			if err != nil {
				return nil, err
			}
			if ok {
				withSession = append(withSession, filled)
			}
		}
		if len(withSession) == 0 {
			return nil, fmt.Errorf("session %q is not a WeChat session for the selected target", opts.SessionID)
		}
		return chooseWeChatTarget(withSession, opts, "session")
	}

	withLatest := make([]wechatSendTarget, 0, len(targets))
	for _, t := range targets {
		filled, ok, err := attachLatestWeChatSession(ctx, st, t)
		if err != nil {
			return nil, err
		}
		if ok {
			withLatest = append(withLatest, filled)
		}
	}
	if len(withLatest) == 0 {
		if len(targets) == 1 {
			t := targets[0]
			return nil, fmt.Errorf("WeChat account %s is connected, but no prior WeChat session was found; send the agent a WeChat message once or pass --chat-id", t.AccountID)
		}
		return nil, errors.New("multiple WeChat accounts are connected but none has a prior session; pass --account-id and --chat-id")
	}

	sort.SliceStable(withLatest, func(i, j int) bool {
		return withLatest[i].UpdatedAt.After(withLatest[j].UpdatedAt)
	})
	return &withLatest[0], nil
}

func collectWeChatSendTargets(ctx context.Context, st store.Store, agentRef string) ([]wechatSendTarget, error) {
	agents, err := agentcli.List(ctx, st)
	if err != nil {
		return nil, err
	}
	agentsByID := make(map[string]store.AgentRecord, len(agents))
	for _, ag := range agents {
		agentsByID[ag.ID] = ag
	}

	selectedAgentID := ""
	if agentRef != "" {
		ag, err := agentcli.Resolve(ctx, st, agentRef)
		if err != nil {
			return nil, err
		}
		selectedAgentID = ag.ID
	}

	chs, err := st.ListAllChannels(ctx)
	if err != nil {
		return nil, err
	}
	var out []wechatSendTarget
	for _, ch := range chs {
		if ch.Type != "wechat" || !ch.Enabled {
			continue
		}
		if selectedAgentID != "" && ch.AgentID != selectedAgentID {
			continue
		}
		ag, ok := agentsByID[ch.AgentID]
		if !ok {
			continue
		}
		// v0.47.0 migrated WeChat bindings out of the legacy configs table
		// into the dedicated channels table (one row per type+account), so
		// read the credential fields straight off the ChannelRecord instead
		// of decoding the old accounts-map blob.
		if ch.AccountID == "" || ch.BotToken == "" {
			continue
		}
		userID := ch.UserID
		if userID == "" {
			userID = ag.UserID
		}
		out = append(out, wechatSendTarget{
			Agent:     ag,
			UserID:    userID,
			AccountID: ch.AccountID,
			BotToken:  ch.BotToken,
			BaseURL:   ch.BaseURL,
			ILinkUser: ch.PlatformUserID,
			UpdatedAt: ch.UpdatedAt,
		})
	}

	if agentRef == "" {
		out = preferDefaultAgentIfUseful(out)
	}
	return out, nil
}

func preferDefaultAgentIfUseful(targets []wechatSendTarget) []wechatSendTarget {
	if len(targets) <= 1 {
		return targets
	}
	agentIDs := map[string]bool{}
	for _, t := range targets {
		agentIDs[t.Agent.ID] = true
	}
	if len(agentIDs) <= 1 {
		return targets
	}
	defaults := filterWeChatTargets(targets, func(t wechatSendTarget) bool {
		return strings.EqualFold(t.Agent.Name, "default")
	})
	if len(defaults) > 0 {
		return defaults
	}
	return targets
}

func attachWeChatSessionByKey(ctx context.Context, st store.Store, target wechatSendTarget, sessionKey string) (wechatSendTarget, bool, error) {
	userIDs := []string{target.UserID}
	if target.UserID == "" {
		pairs, err := st.ListSessionOwnerPairs(ctx)
		if err != nil {
			return target, false, err
		}
		userIDs = userIDs[:0]
		for _, p := range pairs {
			if p.AgentID == target.Agent.ID {
				userIDs = append(userIDs, p.UserID)
			}
		}
	}
	for _, userID := range userIDs {
		ch, acc, chat, err := st.LookupSessionTriple(ctx, userID, target.Agent.ID, sessionKey)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return target, false, err
		}
		if ch == "wechat" && acc == target.AccountID && chat != "" {
			target.UserID = userID
			target.ChatID = chat
			target.SessionKey = sessionKey
			if rec, err := st.GetSession(ctx, userID, target.Agent.ID, sessionKey); err == nil && rec != nil {
				target.UpdatedAt = rec.UpdatedAt
			}
			return target, true, nil
		}
	}
	return target, false, nil
}

func attachLatestWeChatSession(ctx context.Context, st store.Store, target wechatSendTarget) (wechatSendTarget, bool, error) {
	userIDs := []string{target.UserID}
	if target.UserID == "" {
		pairs, err := st.ListSessionOwnerPairs(ctx)
		if err != nil {
			return target, false, err
		}
		userIDs = userIDs[:0]
		for _, p := range pairs {
			if p.AgentID == target.Agent.ID {
				userIDs = append(userIDs, p.UserID)
			}
		}
	}

	var best *store.SessionMeta
	bestUser := ""
	for _, userID := range userIDs {
		sessions, err := st.ListSessions(ctx, userID, target.Agent.ID)
		if err != nil {
			return target, false, err
		}
		for _, s := range sessions {
			if s.Channel != "wechat" || s.AccountID != target.AccountID || s.ChatID == "" {
				continue
			}
			if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
				cp := s
				best = &cp
				bestUser = userID
			}
		}
	}
	if best == nil {
		return target, false, nil
	}
	target.UserID = bestUser
	target.ChatID = best.ChatID
	target.SessionKey = best.Key
	target.UpdatedAt = best.UpdatedAt
	return target, true, nil
}

func chooseWeChatTarget(targets []wechatSendTarget, opts wechatSendOptions, contextLabel string) (*wechatSendTarget, error) {
	targets = filterWeChatTargets(targets, func(t wechatSendTarget) bool {
		return t.ChatID != ""
	})
	if len(targets) == 0 {
		return nil, fmt.Errorf("no WeChat target resolved from %s", contextLabel)
	}
	if len(targets) == 1 {
		return &targets[0], nil
	}
	if opts.AgentRef == "" {
		targets = preferDefaultAgentIfUseful(targets)
		if len(targets) == 1 {
			return &targets[0], nil
		}
	}
	if opts.AccountID == "" {
		accountIDs := uniqueWeChatValues(targets, func(t wechatSendTarget) string { return t.AccountID })
		if len(accountIDs) > 1 {
			return nil, fmt.Errorf("multiple WeChat accounts match; pass --account-id (%s)", strings.Join(accountIDs, ", "))
		}
	}
	agentIDs := uniqueWeChatValues(targets, func(t wechatSendTarget) string { return t.Agent.ID })
	if opts.AgentRef == "" && len(agentIDs) > 1 {
		return nil, fmt.Errorf("multiple WeChat-enabled agents match; pass --agent (%s)", strings.Join(agentIDs, ", "))
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].UpdatedAt.After(targets[j].UpdatedAt)
	})
	return &targets[0], nil
}

func filterWeChatTargets(targets []wechatSendTarget, keep func(wechatSendTarget) bool) []wechatSendTarget {
	out := targets[:0]
	for _, t := range targets {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

func uniqueWeChatValues(targets []wechatSendTarget, value func(wechatSendTarget) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		v := value(t)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
