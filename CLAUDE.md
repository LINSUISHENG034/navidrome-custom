# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Navidrome is an open-source, self-hosted music streaming server written in Go with a React frontend. It implements the Subsonic API for compatibility with mobile/desktop clients and has its own Native REST API for the web UI. Database is SQLite.

## Build & Development Commands

```bash
# First-time setup (installs Go deps, Node deps, golangci-lint, git hooks)
make setup

# Run in dev mode with hot-reload (frontend + backend, port 4533)
make dev

# Build production binary (compiles frontend first, then Go)
make build

# Run Go tests (all packages)
make test

# Run a single Go package's tests
make test PKG=./core/playback/...

# Run Go tests in watch mode (Ginkgo)
make watch

# Run JS tests
make test-js

# Run all tests (Go + JS + i18n validation)
make testall

# Lint Go code (golangci-lint)
make lint

# Lint everything (Go + JS + Prettier)
make lintall

# Format code (Prettier + goimports + go mod tidy)
make format

# Regenerate Wire dependency injection
make wire

# Run code generation (go generate + plugin PDK)
make gen

# Update snapshot tests
make snapshots

# Create a new SQL migration
make migration-sql name=my_migration

# Create a new Go migration
make migration-go name=my_migration

# Download sample music for testing
make get-music
```

Build tags required for Go compilation: `netgo,sqlite_fts5`

### Docker Build

7-stage multi-stage Dockerfile: `osxcross` → `xx-build` → `taglib-build` → `ui` → `base` → `build` → `final`.

```bash
# Build Docker image and load into local daemon
docker buildx build \
  --platform linux/amd64 \
  --build-arg GIT_TAG=v0.60.3-bt \
  --build-arg GIT_SHA=$(git rev-parse --short HEAD) \
  --tag navidrome-bt:dev \
  --load \
  .
```

Key notes:
- `make docker-image` does NOT include `--load`, so the image stays in buildx cache only — use `docker buildx build --load` directly when you need the image in `docker images`
- The frontend (React UI) is embedded into the Go binary via `go:embed` — there is no `/app/static/` directory in the final image. To verify UI code inclusion, use `strings /app/navidrome | grep <pattern>`
- Go is not installed on the host; compilation happens inside the Docker build stages
- Final image is Alpine 3.20 with runtime deps: `ffmpeg`, `mpv`, `sqlite`, `pulseaudio-utils`
- Build args: `GIT_TAG`, `GIT_SHA`, `CROSS_TAGLIB_VERSION` (default `2.2.0-1`)

## Architecture

### Layered Backend

```
model/          → Domain types + repository interfaces (no implementations)
persistence/    → SQLite implementations of model interfaces (squirrel + dbx)
core/           → Business logic services (artwork, playback, ffmpeg, scrobbler, agents)
  └── playback/bluetooth/ → Bluetooth device discovery via PulseAudio
server/         → HTTP layer (chi router)
  ├── subsonic/ → Subsonic API (XML/JSON, /rest/*)
  ├── nativeapi/→ REST API for web UI (/api/*)
  ├── public/   → Public share endpoints (/share/*)
  └── events/   → SSE event broker
adapters/       → External service integrations (lastfm, spotify, taglib)
scanner/        → Multi-phase library scanner pipeline (go-pipeline)
plugins/        → WebAssembly plugin system (Extism/Wazero)
cmd/            → Cobra CLI + Wire DI wiring
conf/           → Viper-based configuration (singleton: conf.Server)
```

### Dependency Injection

Google Wire is used for compile-time DI. Injector definitions are in `cmd/wire_injectors.go`, generated code in `cmd/wire_gen.go`. Key factory functions: `CreateServer()`, `CreateSubsonicAPIRouter()`, `CreateNativeAPIRouter()`, `CreateScanner()`, `GetPlaybackServer()`. Run `make wire` after changing provider sets.

### Database

SQLite with `pocketbase/dbx` (query executor) and `Masterminds/squirrel` (SQL builder). No ORM. Migrations use Goose (`db/migrations/`). All repositories embed `sqlRepository` from `persistence/sql_base_repository.go` which provides common CRUD, filtering, sorting, and pagination.

### Configuration

`conf/configuration.go` — Viper-based, loaded from TOML/env vars (prefix `ND_`)/CLI flags. Global singleton `conf.Server`. Config hooks via `conf.AddHook()`.

### Scanner

Multi-phase pipeline in `scanner/`:
1. Walk directories, detect changes, import metadata
2. Detect missing files and moves
3. Refresh album metadata (parallel with phase 4)
4. Import/update playlists
5. Post-phases: GC, stats refresh, DB optimize

### Playback (Jukebox)

`core/playback/` — Server-side audio via MPV subprocess + IPC. Exposed through both Subsonic `jukeboxControl` and Native API (`/api/jukebox/devices`).

Bluetooth support:
- `core/playback/bluetooth/discovery.go` — PulseAudio-based Bluetooth device auto-discovery via `pactl`
- `server/nativeapi/jukebox_devices.go` — REST endpoints: `GET /api/jukebox/devices`, `PUT /api/jukebox/devices` (switch), `POST /api/jukebox/devices/refresh`
- `ui/src/audioplayer/DeviceSelector.jsx` — Frontend device selector component in player toolbar
- Config: `conf.Server.Jukebox.AutoDiscoverBluetooth` (`ND_JUKEBOX_AUTODISCOVERBLUETOOTH=true`)
- Docker runtime requires PulseAudio socket passthrough, dbus, and `/dev/snd` — see `contrib/docker-compose/docker-compose.bluetooth.yml`

### Frontend

React 17 + react-admin v3 SPA in `ui/`. Built with Vite. State management via Redux + redux-saga. Material UI v4 for components. Key areas: `audioplayer/` (player controls), `album/`, `artist/`, `song/`, `playlist/` (CRUD views), `dataProvider/` (REST client), `subsonic/` (Subsonic API client).

### Plugin System

WASM plugins via Extism SDK. Plugin manager in `plugins/manager.go`. Host functions in `plugins/host_*.go`. PDK code generator at `plugins/cmd/ndpgen/`. Capabilities: metadata agents, scrobblers, schedulers.

## Testing

- Go: Ginkgo v2 + Gomega (BDD-style). Each package has a `*_suite_test.go` bootstrap file.
- Persistence tests use in-memory SQLite with seeded test data.
- Snapshot tests use `bradleyjkemp/cupaloy`.
- Frontend: Vitest + Testing Library. Run with `cd ui && npm test` (single run) or `npm run test:watch`.

## Tooling Versions

- Go: 1.25.0 (see `go.mod`)
- Node: v24 (see `.nvmrc`)
- golangci-lint: v2.10.0 (installed by `make setup`)

## Key Patterns

- Singletons via `utils/singleton.GetInstance()` (PlaybackServer, PluginManager, SSE Broker, etc.)
- Subsonic API handlers return `(*responses.Subsonic, error)` and are registered for both `/path` and `/path.view`
- Native API uses `deluan/rest` for generic CRUD resource registration
- External service adapters register themselves via `init()` side-effects in `adapters/`
- Config env vars use `ND_` prefix (e.g., `ND_MUSICFOLDER`, `ND_PORT`)

## Bluetooth Playback — Lessons Learned

### Known Issues (Fixed)

1. **Frontend auth**: `DeviceSelector.jsx` must use `httpClient` from `dataProvider/httpClient.js` (carries `X-ND-Authorization` JWT header), NOT native `fetch` with `credentials: 'same-origin'`. Navidrome does not use cookie-based auth.

2. **Device discovery timing**: `ListDevices()` must call `mergeBluetoothDevices()` on each invocation, not just at startup. Bluetooth devices may connect/disconnect at any time after the server starts.

3. **PWA cache**: Navidrome is a PWA with Service Worker. After rebuilding the Docker image with UI changes, users must unregister the Service Worker (DevTools → Application → Service Workers → Unregister) and hard-refresh (Ctrl+Shift+R) to load the new JS bundle.

### Known Issues (Open)

4. **Client vs Jukebox mode**: The DeviceSelector only switches the Jukebox output device. It does NOT switch the playback mode from client-side to Jukebox. Users clicking a Bluetooth device still hear audio from their browser. Implementing seamless mode switching requires Native API Jukebox control endpoints and frontend playback mode toggling — see `docs/plans/PLAN-bluetooth-auto-connect.md` Phase 6.

5. **Bluetooth connection instability**: Bluetooth devices auto-disconnect when idle (no audio stream). PulseAudio/PipeWire suspends the sink, and the BT device drops. This is a host-level issue, not a Navidrome bug.

### Architecture Notes

- Jukebox device routes are registered under `adminOnlyMiddleware` — only admin users can access `/api/jukebox/devices`
- `serve_index.go` injects `jukeboxEnabled` into `__APP_CONFIG__` for the frontend; verify with `curl -sL http://host:port/app/ | grep APP_CONFIG`
- Navidrome has two playback paths: client-side (`/rest/stream` → browser `<audio>`) and server-side Jukebox (`jukeboxControl` → MPV subprocess). The Web UI currently only implements client-side playback; Jukebox is only accessible via Subsonic API from third-party clients
