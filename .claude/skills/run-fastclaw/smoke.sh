#!/usr/bin/env bash
# run-fastclaw smoke driver — the verified agent path from the 2026-08-20
# bug-hunt session. Builds from the worktree, installs to ~/.local/bin,
# restarts the daemon, then drives ONE real LLM turn through the gateway
# (apikey create → fastclaw chat → apikey delete) and asserts on the
# startup log. Exits non-zero on any failed assertion.
#
# Usage:  bash .claude/skills/run-fastclaw/smoke.sh
# Env:    PREFIX (default ~/.local) — install destination
#         PROBE_MSG (default 只回复两个字：收到) — the turn's prompt
#
# NOTE: this restarts the LIVE daemon (~1-3s channel outage; DingTalk
# stream has no replay — don't run mid-conversation).
set -euo pipefail

FC_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$FC_ROOT"

PREFIX="${PREFIX:-$HOME/.local}"
FC_BIN="$PREFIX/bin/fastclaw"
LOG="$HOME/.fastclaw/logs/gateway.log"
PROBE_MSG="${PROBE_MSG:-只回复两个字：收到}"

step() { printf '\n==> %s\n' "$*"; }

# ---------------------------------------------------------------- build
step "build (bundle-skills + go build, reusing internal/setup/web)"
# Full `make install` would also run build-web (pnpm install + next
# build). The embedded web assets in internal/setup/web are untracked
# build output that already exists in this worktree; regenerating is
# only needed when web/ itself changed.
VERSION="$(git describe --tags --always --dirty)"
COMMIT="$(git rev-parse --short HEAD)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
make bundle-skills
CGO_ENABLED=0 go build -ldflags "-s -w \
  -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE \
  -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Version=$VERSION \
  -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Commit=$COMMIT \
  -X github.com/fastclaw-ai/fastclaw/internal/buildinfo.Date=$DATE" \
  -o bin/fastclaw ./cmd/fastclaw

# ------------------------------------------------------- install + restart
step "install to $FC_BIN (old binary backed up)"
if [ -x "$FC_BIN" ]; then
  cp "$FC_BIN" "$FC_BIN.bak-$(date +%m%d-%H%M%S)"
fi
install -d "$PREFIX/bin"
install -m 0755 bin/fastclaw "$FC_BIN"

LOG_LINES_BEFORE=0
[ -f "$LOG" ] && LOG_LINES_BEFORE=$(wc -l < "$LOG" | tr -d ' ')

step "daemon restart"
"$FC_BIN" daemon restart

# macOS has no `timeout(1)` — poll manually.
ok=""
for i in $(seq 1 30); do
  status="$(curl -s -m 2 http://127.0.0.1:18953/api/status || true)"
  if printf '%s' "$status" | grep -q "\"version\":\"$VERSION\"" && \
     printf '%s' "$status" | grep -q '"running":true'; then
    ok=1; break
  fi
  sleep 1
done
[ -n "$ok" ] || { echo "FAIL: daemon did not come back on $VERSION"; exit 1; }
echo "daemon up: $VERSION"

# ------------------------------------------------------------ log asserts
sleep 2  # let agents load + MCP connect
NEW_LOG="$( [ "$LOG_LINES_BEFORE" -gt 0 ] && tail -n +"$((LOG_LINES_BEFORE + 1))" "$LOG" || cat "$LOG" )"

errors="$(printf '%s' "$NEW_LOG" | grep -c 'level=ERROR' || true)"
[ "$errors" -eq 0 ] || { echo "FAIL: $errors level=ERROR lines since restart"; printf '%s\n' "$NEW_LOG" | grep 'level=ERROR' | head -5; exit 1; }

loaded="$(printf '%s' "$NEW_LOG" | grep -c 'msg="loaded agent"' || true)"
[ "$loaded" -ge 1 ] || { echo "FAIL: no 'loaded agent' line since restart"; exit 1; }
echo "startup clean: $loaded agent(s) loaded, 0 errors"

mcp="$(printf '%s' "$NEW_LOG" | grep -c 'connected to MCP server' || true)"
echo "mcp connections: $mcp"

# --------------------------------------------------- drive one live turn
step "probe one real turn (temp key → chat → delete key)"
# NB: agent names can contain spaces ("A-Share Strategist"), which
# shifts awk columns — match the agt_<hex> ID by regex instead.
AGENT_ID="$("$FC_BIN" agents list | awk 'NR>1 && match($0, /agt_[a-f0-9]+/) {print substr($0, RSTART, RLENGTH); exit}')"
[ -n "$AGENT_ID" ] || { echo "FAIL: no agent found in agents list"; exit 1; }
echo "agent: $AGENT_ID"

KEY_OUT="$("$FC_BIN" apikey create --name "smoke-$(date +%s)")"
KEY_ID="$(printf '%s\n' "$KEY_OUT" | grep -o 'id=k_[a-f0-9]*' | head -1 | cut -d= -f2)"
KEY_TOKEN="$(printf '%s\n' "$KEY_OUT" | grep -o 'fc_[A-Za-z0-9]*' | head -1)"
cleanup_key() { [ -n "$KEY_ID" ] && "$FC_BIN" apikey delete --id "$KEY_ID" >/dev/null 2>&1 || true; }
trap cleanup_key EXIT

reply="$(FASTCLAW_API_KEY="$KEY_TOKEN" "$FC_BIN" chat -q "$PROBE_MSG" -a "$AGENT_ID" 2>&1 | tail -n 1)"
cleanup_key; trap - EXIT
echo "reply: $reply"
[ -n "$reply" ] || { echo "FAIL: empty chat reply"; exit 1; }

step "SMOKE PASS ✅  build=$VERSION  agent=$AGENT_ID  reply_len=${#reply}"
