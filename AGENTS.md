# Repository Guidelines

## Project Structure & Module Organization

Navidrome is a Go backend with a React/Vite frontend. Backend entry points and Wire dependency injection live in `cmd/`; configuration is in `conf/`; domain interfaces are in `model/`; SQLite persistence is in `persistence/`; business logic is in `core/`; HTTP APIs are under `server/`; scanning lives in `scanner/`; and shared helpers are in `utils/`. The UI app is in `ui/src`, with static assets in `ui/public` and build output in `ui/build`. Database migrations live in `db/migrations`; plugin host code, PDKs, examples, and fixtures are under `plugins/`.

## Build, Test, and Development Commands

- `make setup`: install Go/Node dependencies, golangci-lint, and Git hooks.
- `make dev`: run backend and frontend hot reload on port `4533`.
- `make build`: build the frontend, then compile the Go binary with required build tags.
- `make test`: run Go tests; narrow scope with `make test PKG=./server/...`.
- `make test-js`: run Vitest for `ui/`.
- `make testall`: run Go, frontend, and i18n validation tests.
- `make lintall`: run golangci-lint, Prettier checks, and ESLint.
- `make format`: apply Prettier, `goimports`, and `go mod tidy`.
- `make wire` or `make gen`: update generated DI or plugin/API code.

Go builds use tags `netgo,sqlite_fts5`. Node version is defined in `.nvmrc`; Go version is defined in `go.mod`.

## Coding Style & Naming Conventions

Use `gofmt`/`goimports` for Go and Prettier for UI code. Keep Go packages lowercase and focused by layer. Do not hand-edit generated Go files. UI tests use the existing `*.test.js`/`*.test.jsx` pattern and React component naming already present in `ui/src`.

## Testing Guidelines

Go tests use `testing` plus Ginkgo/Gomega in many packages; suites typically include `*_suite_test.go`. Snapshot tests use Cupaloy and can be refreshed with `make snapshots`. Frontend tests use Vitest and Testing Library. Add focused tests beside changed code, then run the narrow package or UI test before broader checks.

## Commit & Pull Request Guidelines

Follow the project’s conventional format: `<type>(scope): <description>`, for example `fix(ui): stabilize queue highlighting`. Common types include `feat`, `fix`, `docs`, `refactor`, `test`, `build`, and `chore`. Sign commits with `git commit --signoff`. PRs should link the issue, summarize behavior changes, list tests, and include screenshots or videos for UI changes.

## Security & Configuration Tips

Do not commit local runtime data, logs, build output, `.omx/`, secrets, or personal `navidrome.toml` values. Runtime settings are usually configured through TOML, CLI flags, or `ND_` variables such as `ND_MUSICFOLDER` and `ND_PORT`.

## Fork & Custom Documentation

Keep documentation for fork-specific or custom modifications under `fork/docs/` by default, especially implementation plans, upgrade notes, merge notes, and local operational guidance. Prefer this location over upstream-owned directories such as `docs/` unless the documentation is intended to be contributed upstream.

## Agent Execution

Before changing code, configuration, or tracked guidance, read [the execution guide](fork/docs/agent-execution.md) to choose the verification scope, evidence, review, and delivery steps. Keep detailed procedures there rather than duplicating them in always-loaded instructions.

Use [CONTEXT.md](CONTEXT.md) for domain terminology, especially playback, control takeover, and output migration. Local tickets under `.scratch/` and instructions under `docs/agents/` may be available when supplied with a task, but are not checkout prerequisites.

For authorized commits and backups, verify the current task branch and destination remote. Push that branch, not a hardcoded `main` or `master`; stop on conflicts rather than force-pushing or merging the default branch. Production, hardware, and release actions require their own authorization.
