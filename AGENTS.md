# Repository Guidelines

## Project Structure & Module Organization

FastClaw is a Go agent runtime with a Next.js administration UI. The CLI entrypoint is `cmd/fastclaw`; backend packages are under `internal/`. The frontend is in `web/src`, with routes in `web/src/app`, shared components in `web/src/components`, and helpers in `web/src/lib`. Bundled skills are in `skills/`, plugins in `plugins/`, bridge utilities in `tools/`, documentation in `docs/`, and UI previews in `previews/`.

## Build, Test, and Development Commands

- `make build` builds the web UI, syncs bundled skills, and produces `bin/fastclaw`.
- `make test` runs the complete Go test suite with `go test ./...`.
- `make dev` builds web assets and starts the Go server through `air`.
- `make build-web` builds the Next.js export and copies it into the embedded setup assets.
- `cd web && pnpm lint` checks frontend code with ESLint; `pnpm dev` starts the Next.js UI locally.
- `make release-local` creates cross-platform archives under `dist/`.

Use Go 1.25+ and pnpm (`web/pnpm-lock.yaml`). Run `make bundle-skills` after changing a bundled skill to refresh its embedded copy.

## Coding Style & Naming Conventions

Format Go with `gofmt`; use lowercase, descriptive package/file names and `Test...` test names. Frontend code is TypeScript/TSX: follow ESLint conventions, use PascalCase React components and camelCase variables/functions, and align route directories with URL paths. Avoid unrelated generated output, especially under `internal/setup/web`.

## Testing Guidelines

Add focused Go tests beside the package under test with the `_test.go` suffix; prefer table-driven tests for input variations. Run `make test` before backend submissions. For frontend changes, run `cd web && pnpm lint` and `pnpm build`, then manually verify affected interactive routes. No frontend coverage threshold is configured.

## Commit & Pull Request Guidelines

Recent commits use short subjects with prefixes such as `feat:` and `fix:`; keep subjects concise and explain impact in the body when needed. Pull requests should describe the problem and solution, list validation commands, link the issue, and include screenshots or recordings for UI changes. Call out configuration, migration, deployment, or security implications.

## Security & Configuration Tips

Do not commit API keys, tokens, local databases, or release artifacts. Runtime settings use `FASTCLAW_*` variables and the database/UI; review sandbox and bind settings when testing untrusted input.
