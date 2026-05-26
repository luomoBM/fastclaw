# Repository Guidelines

## Project Structure & Module Organization

FastClaw is a Go 1.25 agent runtime with a Next.js dashboard. CLI entry points live in `cmd/fastclaw/`. Core backend packages are under `internal/`, grouped by domain such as `agent`, `api`, `gateway`, `store`, `provider`, `sandbox`, `channels`, and `toolproviders`. The web UI is in `web/`, with routes in `web/src/app/`, components in `web/src/components/`, hooks in `web/src/hooks/`, and client helpers in `web/src/lib/`. Bundled skills live in `skills/`; embedded runtime copies are refreshed under `internal/agent/bundled_skills/` by `make bundle-skills`. Example plugins are in `plugins/`, deployment manifests in `deploy/`, and release/dev helpers in `scripts/`.

## Build, Test, and Development Commands

- `make test`: runs `go test ./...` for all Go packages.
- `make build`: builds the static dashboard, syncs bundled skills, and writes `bin/fastclaw`.
- `make build-web`: installs web dependencies and exports the Next.js app.
- `make bundle-skills`: refreshes embedded bundled skills from `skills/`.
- `make dev`: rebuilds the web app, then starts `air` for local Go development.
- `cd web && pnpm lint`: runs the Next.js ESLint config.
- `cd web && pnpm dev`: starts the dashboard development server.

## Coding Style & Naming Conventions

Format Go with `gofmt`; use tabs for Go indentation and keep package names short, lowercase, and domain-oriented. Test files use the `_test.go` suffix. TypeScript/React uses 2-space indentation, named exports for shared helpers, PascalCase for components, and kebab-case route directories. Keep generic UI primitives in `web/src/components/ui/`.

## Testing Guidelines

Prefer table-driven Go unit tests near the package under test. Run `make test` before backend changes and `cd web && pnpm lint` before UI changes. For generated dashboard assets, run `make build-web` or `make build` to catch export-time failures. Add focused regressions for provider, store, sandbox, gateway, and agent-loop behavior.

## Commit & Pull Request Guidelines

Recent history uses Conventional Commit style with scopes, for example `feat(cmd/fastclaw): add wechat command` and `fix(provider): wedge-proof LLM HTTP client`. Keep commits imperative, scoped, and small. Pull requests should explain behavior changes, list verification commands, link issues, and include screenshots for dashboard UI changes. Call out migrations, new environment variables, and deployment manifest changes.

## Security & Configuration Tips

Do not commit API keys, provider tokens, database files, or local `FASTCLAW_HOME` contents. Runtime bootstrap belongs in `FASTCLAW_*` environment variables; user-facing settings should be changed through the dashboard or CLI configuration commands.
