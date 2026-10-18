# Navidrome Beam: Upstream Synchronization Runbook

This guide documents the architecture seams, touched upstream files, and step-by-step procedures required to rebase **Navidrome Beam** against upstream releases of [Navidrome](https://github.com/navidrome/navidrome).

---

## 1. Architectural Philosophy: Minimal Blast Radius

Navidrome Beam is designed with **modular isolation**:
- Core streaming enhancements reside in self-contained packages (`core/playback/pipewire/`, `core/playback/bluetooth/`).
- Frontend remote player controls are encapsulated in standalone components and Redux sagas (`ui/src/audioplayer/RemotePlayerController.jsx`, `ui/src/audioplayer/jukeboxQueueSaga.js`).
- Upstream source files are modified only through minimal, well-defined integration seams.

---

## 2. Upstream Seam Inventory & Blast Radius

When rebasing against `upstream/master` (or upstream release tags), the following upstream files are the only ones with modifications:

### A. Frontend Seams (`ui/src/`)

| File | Purpose of Beam Modification | Rebase Attention |
|------|------------------------------|------------------|
| `ui/src/store/createAdminStore.js` | Injects `fork(jukeboxQueueSaga)` into root Redux saga chain. | Verify saga middleware initialization remains unchanged. |
| `ui/src/audioplayer/Player.jsx` | Renders `RemotePlayerController` when remote device is active; delegates global media hotkeys to remote jukebox. | Check for upstream changes to audio player container layout or key bindings. |
| `ui/src/audioplayer/DeviceSelector.jsx` | Adds safe session rehydration guard and integrates Bluetooth discovery devices into device dropdown. | Check for updates to device selection dropdown or audio output state. |

### B. Backend Seams (`core/` & `server/`)

| File | Purpose of Beam Modification | Rebase Attention |
|------|------------------------------|------------------|
| `core/playback/mpv/track.go` | Implements `preparePausedSeek` hook and nil-safety guards on track transition. | Verify upstream MPV track loading lifecycle and IPC commands. |
| `server/nativeapi/native_api.go` | Registers `/api/jukebox/*` and `/api/bluetooth/*` routes under native REST API router. | Verify route registration hooks and middleware compatibility. |
| `server/serve_index.go` | Injects `jukeboxEnabled` flag into global `__APP_CONFIG__` in HTML template response. | Verify `__APP_CONFIG__` serialization and response structure. |

### C. Build & Packaging Seams

| File | Purpose of Beam Modification | Rebase Attention |
|------|------------------------------|------------------|
| `Dockerfile` | Installs `pipewire-tools` and `pulseaudio-utils` packages in the final Alpine runtime layer. | Ensure Alpine package installation line retains audio utilities. |

---

## 3. Dedicated Beam Components (Independent Modules)

These files are unique to Navidrome Beam and do not modify upstream code:

- **`core/playback/pipewire/`**: PipeWire stream supervisor, dynamic link manager, and fail-closed channel wiring.
- **`core/playback/bluetooth/`**: D-Bus interface for BlueZ device pairing, trust, and connection state.
- **`server/nativeapi/jukebox_devices.go`**, **`jukebox_session.go`**, **`jukebox_control.go`**: REST endpoints for remote hardware management.
- **`server/nativeapi/bluetooth_devices.go`**: REST endpoints for Bluetooth scanning and connection management.
- **`ui/src/audioplayer/RemotePlayerController.jsx`**: Zero-audio browser console for remote playback.
- **`ui/src/audioplayer/jukeboxQueueSaga.js`**: Redux saga managing queue synchronization with server-side MPV.
- **`ui/src/audioplayer/volumeProfiles.js`**: Perceptual loudness calculation and initial safe gain curves.
- **`contrib/docker-compose/docker-compose.bluetooth.yml`**: Compose orchestration template for PipeWire & PulseAudio.
- **`fork/scripts/native-playback-validation.py`**: Automated end-to-end regression and verification suite.

---

## 4. Step-by-Step Upstream Rebase Procedure

### Step 1: Prepare Local Worktree

Ensure working directory is clean and fetch latest upstream changes:

```bash
git fetch upstream
git checkout beam/main
git status
```

### Step 2: Perform Rebase

Rebase the Beam feature branch on top of upstream master (or target release tag):

```bash
git rebase upstream/master
```

If conflicts arise, they will typically occur in the seam files listed in Section 2:
- If `server/nativeapi/native_api.go` conflicts, preserve route registration for Jukebox and Bluetooth endpoints.
- If `ui/src/store/createAdminStore.js` conflicts, ensure `fork(jukeboxQueueSaga)` remains in the root saga array.
- If `Dockerfile` conflicts, ensure `pipewire-tools` and `pulseaudio-utils` remain in `apk add`.

After resolving conflicts:
```bash
git add <resolved-files>
git rebase --continue
```

### Step 3: Run Validation & Quality Gates

Run all quality gates to guarantee zero regressions:

1. **Frontend Unit & Integration Tests**:
   ```bash
   cd ui
   npm test -- --run
   cd ..
   ```
   *Expectation: 100% passing tests (104+ test suites).*

2. **Backend Concurrency & Race Tests**:
   ```bash
   go test -tags="netgo,sqlite_fts5" -race -count=1 ./core/playback/... ./server/nativeapi/...
   ```
   *Expectation: Clean pass with 0 race warnings.*

3. **Automated Validation Runner**:
   ```bash
   python3 fork/scripts/native-playback-validation.py --all
   ```
   *Expectation: 0 critical failures across playback, UI, and packaging checks.*

4. **Production Build Validation**:
   ```bash
   docker buildx build \
     --platform linux/amd64 \
     --build-arg GIT_TAG=v0.64.2-beam.1.0.0 \
     --build-arg GIT_SHA=$(git rev-parse --short HEAD) \
     --tag navidrome-beam:latest \
     --load .
   ```

---

## 5. Release & Tagging Conventions

- Feature Branch: `beam/main`
- Release Tag Format: `v<UPSTREAM_VERSION>-beam.<MAJOR>.<MINOR>.<PATCH>` (e.g. `v0.64.2-beam.1.0.0`)
- Git Commits: Always include DCO sign-off (`git commit -s`).
