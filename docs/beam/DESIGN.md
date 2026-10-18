# Navidrome Beam: Comprehensive System Architecture & Design Document

**Document:** `docs/beam/DESIGN.md`  
**Version:** 1.0.0 (Release `v0.64.2-beam.1.0.0`)  
**Status:** Living Architectural Specification  
**Target Audience:** Software Engineers, AI Development Agents, and Maintainers  

---

## 1. Executive Summary & Purpose

**Navidrome Beam** is a production-grade, architectural fork of [Navidrome](https://www.navidrome.org) that provides **native, fail-closed server-side Bluetooth and physical hardware audio playback**.

### 1.1 The Problem It Solves
Standard Navidrome is a passive media streaming server designed to stream audio chunks over HTTP to client-side browser `<audio>` elements or mobile apps. When users wanted server-side physical playback (e.g., to living room Bluetooth speakers, soundbars, or audio receivers), they were forced to deploy complex multi-container pipelines:
```text
[Navidrome] ──> [Home Assistant] ──> [Music Assistant (MA)] ──> [Sendspin BT Bridge] ──> [Host PulseAudio] ──> [Bluetooth Speaker]
```
This legacy topology suffered from:
1. **Severe Architectural Bloat**: Consumed 3–4 containers and multiple gigabytes of memory.
2. **Elevated Privilege Hazards**: Required containers with `network_mode: host`, `NET_ADMIN`, `NET_RAW`, and `/dev/bus/usb` device passthrough.
3. **Sound Leakage / Fallback Hazards**: If a Bluetooth speaker disconnected or powered off, PipeWire/WirePlumber default fallback policies automatically rerouted active streams to motherboard audio (`alsa_output.pci-*`), blasting music into server racks or living spaces unexpectedly.
4. **Web UI Desynchronization**: Remote playback was simulated by muting local browser `<audio>` elements, causing queue drift, duplicate playback, and race conditions on tab reload.

### 1.2 The Beam Solution
Navidrome Beam eliminates Home Assistant, Music Assistant, and `sendspin-bt-bridge` completely:
- **Single Container Simplicity**: One container handles library scanning, REST APIs, BlueZ D-Bus integration, server-side MPV playback, and PipeWire link management.
- **Fail-Closed Stream Pinning**: Audio nodes use `node.autoconnect=false` + `node.dont-reconnect=true`. PipeWire link supervision explicitly binds stream channels to the designated Bluetooth sink. If the speaker drops, playback **halts in milliseconds—zero sound leakage to internal sound cards**.
- **Decoupled Dual-Track Web Console**: In remote mode, the browser `<audio>` element, `AudioContext`, and media streaming network sockets are completely unmounted. The Web UI functions as a pure status and command console synchronized with the server's authoritative queue via REST APIs and SSE events.

---

## 2. System Topology & Communication Flow

```text
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Host Linux Operating System                                                     │
│                                                                                 │
│   ┌──────────────────────────────┐       ┌──────────────────────────────────┐   │
│   │ BlueZ D-Bus System Daemon    │       │ PipeWire Audio Server (0.3.48+)  │   │
│   │ /var/run/dbus/system_bus     │       │ /run/user/${UID}/pipewire-0      │   │
│   └──────────────┬───────────────┘       │ /run/user/${UID}/pulse/native    │   │
│                  │                       └────────────────┬─────────────────┘   │
└──────────────────┼────────────────────────────────────────┼─────────────────────┘
                   │ (read-only socket)                     │ (read-only sockets)
┌──────────────────┼────────────────────────────────────────┼─────────────────────┐
│ Container: navidrome-beam (Port 4534)                     │                     │
│                  ▼                                        ▼                     │
│   ┌──────────────────────────────┐       ┌──────────────────────────────────┐   │
│   │ BlueZ Device Controller      │       │ PipeWire Link Supervisor         │   │
│   │ core/playback/bluetooth/     │       │ core/playback/mpv/pipewire.go    │   │
│   └──────────────┬───────────────┘       └────────────────▲─────────────────┘   │
│                  │ (Device scan & pair)                   │ (pw-cli link creation)
│                  ▼                                        │                     │
│   ┌──────────────────────────────┐       ┌────────────────┴─────────────────┐   │
│   │ Native REST API & Broker     │       │ Headless MPV Playback Process    │   │
│   │ server/nativeapi/            │──────>│ (PULSE_PROP_node.autoconnect=0)  │   │
│   │ server/events/               │       └────────────────┬─────────────────┘   │
│   └──────────────▲───────────────┘                        │ (UNIX Pulse socket) │
│                  │ (/api/jukebox/*, SSE)                  ▼                     │
│   ┌──────────────┴───────────────┐       ┌──────────────────────────────────┐   │
│   │ Dual-Track Web UI Console    │       │ Physical Bluetooth Speaker       │   │
│   │ (Zero browser <audio> engine)│       │ (e.g. Aura Studio 5, Soundbar)   │   │
│   └──────────────────────────────┘       └──────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Subsystem Architecture Deep-Dive

### 3.1 Backend Core: Fail-Closed PipeWire Link Supervisor
**Files:** `core/playback/mpv/pipewire.go`, `core/playback/mpv/track.go`, `core/playback/mpv/ipc.go`

#### A. Isolation via Environment Overrides
When Navidrome launches an MPV subprocess to play a track targeted at a Bluetooth device (`pulse/bluez_output.*`), it injects three critical environment variables:
```go
PULSE_PROP_OVERRIDE_node.autoconnect=false
PULSE_PROP_OVERRIDE_node.dont-reconnect=true
PULSE_PROP_OVERRIDE_node.name=navidrome-output-<nonce>
```
- `node.autoconnect=false`: Forbids WirePlumber or PipeWire policy engines from automatically connecting the output stream to the default audio sink.
- `node.dont-reconnect=true`: Prevents PipeWire from rescuing and moving the stream to a fallback soundcard if the Bluetooth sink disappears.
- `node.name`: Assigns a globally unique, cryptographically unguessable name to identify the node in the PipeWire object graph.

#### B. Explicit Channel Link Formation
Unlike standard audio players that rely on PipeWire's daemon-side session manager, Beam acts as its own **explicit link supervisor**:
1. After MPV starts, `pipeWireSnapshot(ctx)` executes `pw-dump --no-colors` to parse the object graph.
2. It verifies the existence and generational identity (`object.serial`) of both the stream node and target Bluetooth sink node (`Audio/Sink`).
3. It spawns a long-lived `pw-cli` process connected via pipe and issues explicit link creation commands:
   ```text
   create-link <stream-name> <output-port> <target-sink> <input-port> { object.linger=false link.passive=false navidrome.output-token=<token> }
   ```
4. Both stereo channels (`FL` and `FR`) are verified. Partial stereo layouts are rejected immediately.
5. `object.linger=false`: Ensures that if the `pw-cli` process exits or crashes, PipeWire automatically cleans up the links immediately.

#### C. Continuous Health & Generation Pinning (`checkSink`)
During playback, `arm()` initiates a background ticker (`conf.Server.Jukebox.OutputCheckInterval`, default 1s):
- Calls `checkSink(ctx)`: Parses graph snapshots to ensure the target sink's ID and `object.serial` have not been replaced (detecting Bluetooth disconnections or device recreation).
- Listens on `output.owner.done`: If the `pw-cli` supervisor process dies, a `PlaybackEventFailure` is immediately dispatched to the player state machine.
- **Fail-Closed Guarantee**: If the Bluetooth device powers off, playback halts within ~1–2 seconds, preventing even a single millisecond of leakage to motherboard speakers.

#### D. Non-Destructive Paused Seek (`preparePausedSeek`)
When paused, invoking `ao-reload` in PipeWire with `node.autoconnect=false` causes MPV's `ao_pulse` driver to uninitialize and hang (as MPV does not recreate Pulse streams while paused).
Beam's `preparePausedSeek()` checks:
```go
if output := t.output.Load(); output != nil {
    ctx, cancel := context.WithTimeout(t.ctx, ipcCallTimeout)
    defer cancel()
    return output.verify(ctx)
}
```
If the PipeWire stream is active, it verifies link topology integrity via `output.verify(ctx)` and executes an exact MPV seek (`seek <pos> absolute+exact`) without destroying or recreating the PipeWire stream.

---

### 3.2 Backend: Native Bluetooth Discovery & Control
**Files:** `core/playback/bluetooth/bluez.go`, `core/playback/bluetooth/discovery.go`, `server/nativeapi/bluetooth.go`

- **Native D-Bus Connection**: Direct Unix domain socket connection to `/var/run/dbus/system_bus_socket`. Communicates with BlueZ (`org.bluez`) without third-party CLI wrappers or Python dependencies.
- **Capabilities**:
  - `List()`: Enumerates paired and discovered audio-capable Bluetooth devices (`UUID 0000110b` / `0000110a`).
  - `Connect(mac)` / `Disconnect(mac)`: Initiates BlueZ `org.bluez.Device1.Connect` calls.
  - `Pair(mac)` / `Trust(mac)` / `Remove(mac)`: Full pairing lifecycle support.
- **REST Surface**:
  - `GET /api/bluetooth`: Lists available Bluetooth devices and connection status.
  - `POST /api/bluetooth/connect`: Triggers connection to a specified MAC address.
  - `POST /api/bluetooth/disconnect`: Disconnects device.

---

### 3.3 Backend: Durable Session Authority & Lifecycle
**Files:** `core/playback/session_manager.go`, `core/playback/device.go`, `core/playback/playbackserver.go`

- **Multi-Client Session Scoping**: Commands are scoped by `{ sessionId, clientId }`.
- **Takeover Semantics**:
  - Multiple browser tabs can attach to the same `sessionId`. The first tab becomes the active `ownerClientId`.
  - Secondary tabs attach as observers. Observers receive real-time playback snapshots but cannot mutate the queue or volume unless they explicitly request takeover (`takeover=true`).
- **Heartbeat Loop**: Clients maintain authority via periodic heartbeat (`POST /api/jukebox/session/heartbeat`). If a client fails to heartbeat within the lease duration (default 30s), ownership expires gracefully without stopping the physical speaker.

---

### 3.4 Frontend: Decoupled Dual-Track Architecture
**Files:** `ui/src/audioplayer/Player.jsx`, `ui/src/audioplayer/RemotePlayerController.jsx`, `ui/src/audioplayer/RemotePlayer.jsx`

#### A. Dual-Track Mode Router
In `ui/src/audioplayer/Player.jsx`:
```jsx
const Player = () => {
  const jukeboxMode = useSelector((state) => state.player.jukeboxMode)
  return (
    <ThemeProvider theme={createMuiTheme(theme)}>
      <GlobalHotKeys handlers={handlers} keyMap={keyMap} allowChanges />
      {jukeboxMode ? <RemotePlayerController /> : <LocalPlayer />}
    </ThemeProvider>
  )
}
```
- `<GlobalHotKeys>` and `<ThemeProvider>` are elevated to the root so keyboard shortcuts (Space to play/pause, Left/Right to seek, Up/Down for volume) work identically across both modes.
- When `jukeboxMode` is `true`, `LocalPlayer` (and its internal `ReactJkMusicPlayer` / `<audio>` element) is completely unmounted.

#### B. Redux Queue Action Forwarding (`jukeboxQueueSaga.js`)
When users click library actions ("Play All", "Play Next", "Add to Queue") in song, album, or playlist views, standard Redux actions are dispatched:
- `PLAYER_PLAY_TRACKS` $\to$ Calls `jukeboxClient.set(trackIds, scope)` $\to$ `jukeboxClient.skip(index, 0, scope)` $\to$ `jukeboxClient.play(scope)`.
- `PLAYER_SET_TRACK` $\to$ Calls `jukeboxClient.set(trackIds, scope)` $\to$ `jukeboxClient.skip(0, 0, scope)` $\to$ `jukeboxClient.play(scope)`.
- `PLAYER_PLAY_NEXT` $\to$ Calls `jukeboxClient.add(trackIds, currentIndex + 1, scope)`.
- `PLAYER_ADD_TRACKS` $\to$ Calls `jukeboxClient.add(trackIds, -1, scope)` (appends to server queue).
- `PLAYER_CLEAR_QUEUE` $\to$ Calls `jukeboxClient.stop(scope)` $\to$ `jukeboxClient.set([], scope)`.

#### C. Safe Session Rehydration (Non-Destructive Reload)
In `ui/src/audioplayer/jukeboxSession.js` (`rehydrateJukeboxSession`):
- When a page is reloaded or the device dropdown is opened, the client queries `GET /api/jukebox/session/status`.
- If an active server session exists on a remote device:
  1. Rehydrates Redux state (`jukeboxMode: true`, current device).
  2. Syncs the server's playlist entry into Redux (`syncQueue`).
  3. **Guarantees that local browser queue tracks are NOT pushed to the server**, preventing stale browser cache from truncating active playback.
- Local queue pushing is strictly constrained to **explicit user switches** from Local $\to$ Remote (`DeviceSelector.jsx:155`):
  ```javascript
  if (!jukeboxMode && trackIds.length > 0) {
    await jukeboxClient.set(trackIds, scope)
    // ...
  }
  ```

---

## 4. Non-Negotiable Invariants & Golden Rules

When developing future enhancements or optimizations, **never violate these 5 core invariants**:

1. **Rule 1: Always Preserve Fail-Closed Output Routing**  
   *Never* enable `node.autoconnect=true` or allow fallback audio routing. If a Bluetooth sink is unavailable, initialization must fail with an explicit error. Audio must **never** leak to host PCI/ALSA audio devices.
2. **Rule 2: Never Mount Browser `<audio>` in Remote Mode**  
   *Never* attempt to keep browser audio elements mounted or "muted" to synchronize time. Remote mode must remain a pure status and command console.
3. **Rule 3: Respect the "Thin Hook, Deep Module" Seam Boundary**  
   *Never* inline large blocks of Beam logic into upstream files like `Player.jsx`, `createAdminStore.js`, or `track.go`. Always place custom logic in dedicated modules (`ui/src/audioplayer/jukebox*.js`, `core/playback/mpv/pipewire.go`).
4. **Rule 4: Do Not Compensate for Test Overhead in Production Constants**  
   *Never* expand production constants (e.g. `trackReadyTimeout = 3 * time.Second`) to accommodate slow CI or TSAN race testing overhead. Test-specific timeouts belong in test fixtures or `conf.Server.Jukebox.OutputPreparationTimeout`.
5. **Rule 5: Maintain Zero Ingestion of Prototype Debt**  
   *Never* commit temporary research notes, spikes, or ad-hoc test scripts to `beam/main`. All architectural documentation belongs in `docs/beam/`.

---

## 5. Future Optimization Opportunities & Extension Seams

For future agents tasked with optimizing Navidrome Beam, the following areas represent high-value improvement seams:

### Seam 1: Persistent PipeWire Graph Monitoring (Replacing `pw-dump` Polling)
- **Current State**: `checkSink()` executes `pw-dump --no-colors` at 1-second intervals.
- **Optimization**: Implement a persistent CGO-free PipeWire Native Protocol listener (or long-lived `pw-mon` / `pw-dump --monitor` parser) using a persistent background worker. This will reduce process spawn overhead and provide sub-100ms disconnect notifications.

### Seam 2: Native In-Process PipeWire Integration (CGO / WASM)
- **Current State**: Uses `/usr/bin/pw-cli` subprocess via stdin/stdout pipe for `create-link`.
- **Optimization**: Use pure Go PipeWire protocol bindings or Extism/Wazero plugins to issue `pw_core_create_object` directly, eliminating external process management.

### Seam 3: Native WebRTC / WebSocket Client Audio Streaming
- **Current State**: Remote mode is strictly server-to-hardware (Bluetooth/PipeWire). Local mode is client-side HTTP streaming.
- **Optimization**: Build a WebRTC/WebSocket audio pipe from server-side MPV to the browser, enabling multi-room synchronized streaming where the browser acts as a synchronized remote speaker.

### Seam 4: Automatic Bluetooth Reconnect & Standby Management
- **Current State**: Requires Bluetooth speakers to remain connected or be manually reconnected via the UI device selector.
- **Optimization**: Add an optional auto-reconnect supervisor in `core/playback/bluetooth/` that listens to BlueZ D-Bus properties and automatically re-establishes connections to trusted speakers when they power on.

---

## 6. Verification & Upstream Sync Runbook

### Running the Full Test Matrix
```bash
# Backend unit tests
go test -tags="netgo,sqlite_fts5" -count=1 ./core/playback/... ./server/nativeapi/... ./conf/...

# Backend concurrent race detector (must pass with 0 race warnings)
GORACE="atexit_sleep_ms=0" go test -tags="netgo,sqlite_fts5" -race -count=1 ./core/playback/... ./server/nativeapi/...

# Frontend unit and integration tests (104 files, 1032 tests)
cd ui && npm test -- --run

# Full automated validation runner
python3 fork/scripts/native-playback-validation.py --stress-count 2 --mpv-null
```

### Performing an Upstream Rebase
Refer to `docs/beam/UPSTREAM_SYNC.md` for detailed instructions on rebasing `beam/main` against `upstream/master`.

---

## 7. Key Post-Validation Records & Benchmark Baseline

The following baseline metrics were formally verified and accepted on release `v0.64.2-beam.1.0.0`:

### 7.1 Automated Software Test Suite
- **Go Backend Core & API**: 100% PASS across `./core/playback/...`, `./server/nativeapi/...`, `./server/subsonic/...`, `./server/events/...`, and `./conf/...`.
- **Concurrency & Race Detection**: Executed via `GORACE="atexit_sleep_ms=0" go test -race -count=1 ./core/playback/... ./server/nativeapi/...`. Completed in 32.6s with **0 data race warnings and 0 errors**.
- **Frontend Dual-Track Web UI**: Executed via `npm test -- --run` in `ui/`. **104 test files passed (100%), 1,032 tests passed (0 failures)**. ESLint passed with 0 errors and 0 warnings.
- **Validation Runner Gate**: Executed via `fork/scripts/native-playback-validation.py`. Preserved in `/tmp/native-playback-validation/run-beam-final/` confirming **`critical_failures: []`**.

### 7.2 Physical Bluetooth Hardware Acceptance (Aura Studio 5)
Tested against physical Aura Studio 5 (`24:C4:06:FA:00:37`, `bluez_output.24_C4_06_FA_00_37.a2dp-sink`):
1. **H01 (Missing Target Fail-Closed)**: Requesting playback while the speaker is powered off was rejected in 0.00s with HTTP 404. Host default sink remained locked to `alsa_output.pci-0000_00_1f.3.iec958-stereo`. **Zero sound played on internal soundcards**.
2. **H02 (Initial Playback & Safe Gain)**: Pinned stereo links formed cleanly. Playback clamped to safe 15% gain (`0.15`). Audible sound played without initial clicks or pop bursts.
3. **H03 (Paused Seek & Resume)**: Paused at 0s, sought to offset 3s while remaining paused, and resumed cleanly from 3s through completion without stream destruction or audio glitch.
4. **H04 (Disconnect during Pause)**: Bluetooth disconnected while paused; server preserved queue and track position. Host default sink was unaffected.
5. **H05 (Live Disconnect during Audible Playback)**: Disconnected during audible playback; disconnect detected in 2.29s, all links and MPV processes retired cleanly, **zero audio spilled over to motherboard soundcards**.
6. **H06 (Natural EOF & Queue Advancement)**: Two consecutive short tracks played sequentially; Track 1 reached natural EOF, Track 2 seamlessly advanced on the same Bluetooth link, and queue completed cleanly.

