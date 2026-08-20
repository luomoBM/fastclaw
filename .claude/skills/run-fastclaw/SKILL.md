---
name: run-fastclaw
description: Build, install, restart, and drive the FastClaw gateway daemon — deploy a worktree build to ~/.local/bin, smoke-test a live agent turn via fastclaw chat, assert on gateway.log, verify a fix on the running daemon. Use when asked to run FastClaw, deploy/restart the daemon, or verify changes against the live gateway.
---

# Run FastClaw

FastClaw is a Go gateway daemon (port 18953) that connects LLM agents to
DingTalk/WeChat channels. The agent path is **one script**:
`.claude/skills/run-fastclaw/smoke.sh` builds from the worktree, installs
to `~/.local/bin`, restarts the daemon, drives one real LLM turn through
the gateway, and asserts zero startup errors. Paths below are relative to
the repo root.

## Prerequisites

macOS + Go ≥1.25 (repo is `go 1.25.0`). Daemon state lives in
`~/.fastclaw/` (logs: `~/.fastclaw/logs/gateway.log`). No apt/pnpm needed
on the fast path — the embedded web assets in `internal/setup/web` are
untracked build output that already exists in this worktree; `go:embed`
uses them as-is.

## Run (agent path) — the driver

```bash
bash .claude/skills/run-fastclaw/smoke.sh
```

One command does: `make bundle-skills` → version-stamped `go build` →
backup+install to `~/.local/bin` → `daemon restart` → poll `/api/status`
for the new version → assert 0 `level=ERROR` + ≥1 `loaded agent` in the
log lines since restart → mint a temp API key → one `fastclaw chat` turn
to the first agent → delete the key. Prints `SMOKE PASS ✅` with
build/agent/reply on success, non-zero exit on any failure.

CAUTION: this restarts the **live** daemon (~1–3s channel outage;
DingTalk stream mode has no replay — messages sent during the outage are
dropped). Don't run it mid-conversation.

Env knobs: `PREFIX` (install root, default `~/.local`), `PROBE_MSG`
(chat prompt, default `只回复两个字：收到`).

### Manual pieces (when you need just one step)

```bash
curl -s http://127.0.0.1:18953/api/status          # version + running + uptime
~/.local/bin/fastclaw agents list                   # NAME ID OWNER (names contain spaces!)
KEY_OUT=$(~/.local/bin/fastclaw apikey create --name probe-$(date +%s))
# token appears ONCE in $KEY_OUT as fc_…; list shows it redacted
FASTCLAW_API_KEY=<token> ~/.local/bin/fastclaw chat -q "只回复两个字：收到" -a agt_d64f2f2c99a242ce9c
~/.local/bin/fastclaw apikey delete --id k_<id>     # --id flag, not positional
tail -n 200 ~/.fastclaw/logs/gateway.log            # agents/MCP/adapters/errors
```

## Build (from the driver, for reference)

```bash
make bundle-skills
CGO_ENABLED=0 go build -ldflags "-s -w \
  -X main.version=$(git describe --tags --always --dirty) \
  -X main.commit=$(git rev-parse --short HEAD) \
  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o bin/fastclaw ./cmd/fastclaw
```

(The driver additionally stamps `internal/buildinfo.*` so the agent
runtime reports the same version as `fastclaw version`.) `make install`
runs `build-web` first — a pnpm install + Next build — and is only needed
when `web/` itself changed; the fast path above deliberately reuses
`internal/setup/web`.

## Test

```bash
go test ./... -count=1        # full suite
go vet ./...
go test -race -count=1 ./internal/channels/ ./internal/agent/ ./internal/mcp/ ./internal/store/ ./internal/gateway/ ./internal/provider/
```

## Gotchas

- **`timeout(1)` does not exist on macOS zsh** — poll with a
  `for i in $(seq 1 30)` + `sleep 1` loop instead (the driver does).
- **`apikey delete` requires `--id k_…`**; a positional arg silently
  errors with "required flag(s) id not set".
- **`apikey create` prints the token exactly once** (list redacts it) —
  capture it in the same pipeline and delete the key when done.
- **Agent names contain spaces** ("A-Share Strategist"), so awk column
  parsing grabs the wrong field — match `/agt_[a-f0-9]+/` by regex.
- **An old daemon keeps serving port 18953** after the binary is
  replaced — always `daemon restart`, then verify the version via
  `/api/status` matches `git describe` before trusting a deploy.
- **`/api/status` shows `"agents":[]`** in its unauthenticated summary —
  use `msg="loaded agent"` lines in gateway.log to verify agent loading.
- Restarting the daemon bounces DingTalk/WeChat adapters; expect
  `starting channel` / `channel lease acquired` lines and a few seconds
  of reconnect.

## Troubleshooting

- **`command not found: timeout`** → macOS; use the driver's poll loop.
- **Driver fails at "daemon did not come back"** → check
  `~/.fastclaw/logs/gateway.log` tail for a startup panic, and
  `pgrep -fl fastclaw` for a stale process holding 18953; `daemon
  restart` again after killing it.
- **Empty chat reply** → the minted key works but the turn failed;
  grep gateway.log for `level=ERROR` (e.g. provider auth/timeout) —
  the driver's log assert runs before the probe, so a mid-turn provider
  failure shows up here.
