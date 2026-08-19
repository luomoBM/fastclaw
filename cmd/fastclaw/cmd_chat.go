package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/cliclient"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/daemon"
	"github.com/fastclaw-ai/fastclaw/internal/tui"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

type chatOptions struct {
	agentID      string
	session      string
	query        string
	baseURL      string
	apiKey       string
	continueLast bool
}

func chatCmd() *cobra.Command {
	var opts chatOptions
	cmd := &cobra.Command{
		Use:   "chat [message]",
		Short: "Chat with a FastClaw agent in the terminal",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.query == "" && len(args) > 0 {
				opts.query = strings.Join(args, " ")
			}
			return runChat(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVarP(&opts.agentID, "agent", "a", "", "agent ID or name")
	cmd.Flags().StringVarP(&opts.session, "resume", "r", "", "resume a session by ID")
	cmd.Flags().BoolVarP(&opts.continueLast, "continue", "c", false, "continue the most recent session")
	cmd.Flags().StringVarP(&opts.query, "query", "q", "", "send one message and exit")
	cmd.Flags().StringVar(&opts.baseURL, "base-url", "", "gateway URL (default http://127.0.0.1:$FASTCLAW_PORT)")
	cmd.Flags().StringVar(&opts.apiKey, "api-key", "", "API key (or FASTCLAW_API_KEY)")
	return cmd
}

func isInteractiveTerminal(in, out *os.File) bool {
	inStat, inErr := in.Stat()
	outStat, outErr := out.Stat()
	return inErr == nil && outErr == nil && inStat.Mode()&os.ModeCharDevice != 0 && outStat.Mode()&os.ModeCharDevice != 0
}

func runChat(ctx context.Context, opts chatOptions) error {
	env := config.LoadEnv()
	port := env.Gateway.Port
	if port == 0 {
		port = 18953
	}
	localGateway := opts.baseURL == ""
	if opts.baseURL == "" {
		opts.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	opts.baseURL = strings.TrimRight(opts.baseURL, "/")
	if opts.apiKey == "" {
		opts.apiKey = os.Getenv("FASTCLAW_API_KEY")
	}
	if opts.query == "" && !isInteractiveTerminal(os.Stdin, os.Stdout) {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			opts.query = strings.TrimSpace(string(data))
		}
	}

	if localGateway {
		if err := ensureGateway(ctx, opts.baseURL, port); err != nil {
			return err
		}
	}
	if opts.apiKey == "" {
		if !localGateway {
			return errors.New("remote chat requires --api-key or FASTCLAW_API_KEY")
		}
		var err error
		opts.apiKey, err = ensureCLIToken(ctx)
		if err != nil {
			return err
		}
	}

	c := cliclient.New(opts.baseURL, opts.apiKey)
	agents, err := c.Agents(ctx)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}
	if len(agents) == 0 {
		return errors.New("no agents configured; create one with `fastclaw agents init <name>`")
	}
	agent, err := selectAgent(agents, opts.agentID)
	if err != nil {
		return err
	}
	resume := opts.session != ""
	if opts.continueLast && opts.session == "" {
		sessions, err := c.Sessions(ctx, agent.ID)
		if err != nil {
			return err
		}
		if len(sessions) > 0 {
			opts.session = sessions[0].ID
			resume = true
		}
	}
	if opts.session == "" {
		opts.session = cliclient.NewSessionID()
	}

	if opts.query != "" {
		return plainStream(ctx, c, agent.ID, opts.session, opts.query, os.Stdout)
	}
	if !isInteractiveTerminal(os.Stdin, os.Stdout) {
		return errors.New("interactive chat requires a terminal; use --query or pipe a message as an argument")
	}
	return tui.Run(tui.Options{
		Client:      c,
		Agent:       agent,
		Agents:      agents,
		SessionID:   opts.session,
		LoadHistory: resume,
		Version:     version,
	})
}

func ensureGateway(ctx context.Context, baseURL string, port int) error {
	if gatewayReady(ctx, baseURL) {
		warnVersionSkew(ctx, baseURL)
		return nil
	}
	st, _ := daemon.GetStatus()
	if st == nil || !st.Running {
		fmt.Fprintln(os.Stderr, "Starting FastClaw gateway…")
		if err := daemon.Start(port); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if gatewayReady(ctx, baseURL) {
			warnVersionSkew(ctx, baseURL)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("gateway did not become ready at %s; check `fastclaw daemon logs`", baseURL)
}

// warnVersionSkew compares the running gateway's version against this
// CLI binary and warns when they differ. The chat client reuses any
// healthy gateway on the port, so after an upgrade or `make install`
// the daemon keeps serving the OLD code until restarted — without this
// check the only symptom is the agent reporting a stale version in
// conversation. Best-effort: any probe failure is silently ignored.
func warnVersionSkew(ctx context.Context, baseURL string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/status", nil)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var st struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&st) != nil {
		return
	}
	if st.Version != "" && st.Version != version {
		fmt.Fprintf(os.Stderr, "⚠ gateway is running FastClaw %s but this CLI is %s — run `fastclaw daemon restart` to pick up the new binary\n", st.Version, version)
	}
}

func gatewayReady(ctx context.Context, baseURL string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func ensureCLIToken(ctx context.Context) (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "cli-token")
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), nil
	}
	st, err := openStoreFromEnv()
	if err != nil {
		return "", err
	}
	defer st.Close()
	accounts, err := users.NewAccounts(st)
	if err != nil {
		return "", err
	}
	list, err := accounts.List(ctx)
	if err != nil {
		return "", err
	}
	owner := ""
	for _, account := range list {
		if account.Role == users.RoleSuperAdmin {
			owner = account.ID
			break
		}
	}
	if owner == "" {
		return "", errors.New("no super_admin found; finish FastClaw onboarding first")
	}
	keys, err := users.NewAPIKeys(st)
	if err != nil {
		return "", err
	}
	_, token, err := keys.Create(ctx, owner, "FastClaw terminal", users.APIKeyTypeUser, nil)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create terminal credential directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("save terminal credential: %w", err)
	}
	return token, nil
}

func selectAgent(agents []cliclient.Agent, wanted string) (cliclient.Agent, error) {
	if wanted == "" {
		// The API currently returns agents newest-first. The terminal's
		// implicit default is the user's first-created agent, independent of
		// the response ordering. Older servers may omit createdAt; preserve
		// their response order in that case.
		selected := agents[0]
		for _, agent := range agents[1:] {
			if !agent.CreatedAt.IsZero() && (selected.CreatedAt.IsZero() || agent.CreatedAt.Before(selected.CreatedAt)) {
				selected = agent
			}
		}
		return selected, nil
	}
	for _, agent := range agents {
		if agent.ID == wanted || strings.EqualFold(agent.Name, wanted) {
			return agent, nil
		}
	}
	return cliclient.Agent{}, fmt.Errorf("agent %q not found", wanted)
}

// plainStream renders one turn as line-oriented text: markdown-rendered
// when stdout is a terminal, clean plain text when piped. Used by the
// one-shot --query / stdin path; the interactive path uses internal/tui.
func plainStream(ctx context.Context, c *cliclient.Client, agentID, sessionID, message string, out io.Writer) error {
	interactive := writerIsTerminal(out)
	statusVisible := false
	wrote := false
	atLineStart := true
	streamedContent := false
	var markdownBuffer strings.Builder
	toolNames := make(map[string]string)
	// Give immediate feedback before the provider emits its first token. The
	// status occupies one replaceable terminal line; pipes/files stay clean.
	if interactive {
		fmt.Fprintf(out, "%s◌ Thinking…%s", ansiIf(true, "\033[36m"), ansiIf(true, "\033[0m"))
		statusVisible = true
	}
	clearStatus := func() {
		if statusVisible {
			fmt.Fprint(out, "\r\033[2K")
			statusVisible = false
		}
	}
	writeText := func(text string) {
		if text == "" {
			return
		}
		clearStatus()
		fmt.Fprint(out, text)
		wrote = true
		atLineStart = strings.HasSuffix(text, "\n")
	}
	flushMarkdown := func(force bool) {
		if !interactive || markdownBuffer.Len() == 0 {
			return
		}
		text := markdownBuffer.String()
		cut := len(text)
		if !force {
			cut = tui.CompleteMarkdownPrefix(text)
			if cut == 0 {
				return
			}
		}
		block := text[:cut]
		markdownBuffer.Reset()
		markdownBuffer.WriteString(text[cut:])
		writeText(tui.RenderMarkdown(block, 100) + "\n")
	}
	writeContent := func(text string) {
		if !interactive {
			writeText(text)
			return
		}
		markdownBuffer.WriteString(text)
		flushMarkdown(false)
	}
	startLine := func() {
		flushMarkdown(true)
		clearStatus()
		if wrote && !atLineStart {
			fmt.Fprintln(out)
		}
		atLineStart = true
	}
	finish := func() {
		flushMarkdown(true)
		clearStatus()
		if wrote && !atLineStart {
			fmt.Fprintln(out)
		}
	}

	err := c.Stream(ctx, agentID, sessionID, message, func(ev cliclient.Event) {
		switch ev.Type {
		case "content_delta":
			// Write every provider delta as it arrives. Buffering until done made
			// the endpoint streaming in name only from a terminal user's view.
			delta := ev.Str("delta")
			writeContent(delta)
			streamedContent = streamedContent || delta != ""
		case "content":
			if !streamedContent {
				writeContent(ev.Str("content"))
			}
			streamedContent = false
		case "tool_call":
			name, id := ev.Str("name"), ev.Str("id")
			if id != "" && name != "" {
				toolNames[id] = name
			}
			if name != "" {
				startLine()
				fmt.Fprintf(out, "%s↳ %s%s\n", ansiIf(interactive, "\033[36m"), name, ansiIf(interactive, "\033[0m"))
				wrote, atLineStart = true, true
				streamedContent = false
			}
		case "tool_result":
			name := toolNames[ev.Str("id")]
			startLine()
			label := "done"
			if name != "" {
				label = name
			}
			fmt.Fprintf(out, "%s✓ %s%s", ansiIf(interactive, "\033[32m"), label, ansiIf(interactive, "\033[0m"))
			if summary := toolResultSummary(ev.Str("result")); summary != "" {
				fmt.Fprintf(out, " %s%s%s", ansiIf(interactive, "\033[2m"), summary, ansiIf(interactive, "\033[0m"))
			}
			fmt.Fprintln(out)
			wrote, atLineStart = true, true
		case "done":
			finish()
		}
	})
	if err != nil {
		clearStatus()
		return err
	}
	return nil
}

func ansiIf(enabled bool, code string) string {
	if enabled && os.Getenv("NO_COLOR") == "" {
		return code
	}
	return ""
}

func toolResultSummary(result string) string {
	result = strings.Join(strings.Fields(result), " ")
	if result == "" {
		return ""
	}
	const max = 96
	if len(result) > max {
		return result[:max-1] + "…"
	}
	return result
}

func writerIsTerminal(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}
