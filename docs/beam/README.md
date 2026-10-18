# Navidrome Beam

**Native Server-Side Bluetooth & Hardware Audio Streaming for Navidrome**

Navidrome Beam is a specialized edition of [Navidrome](https://www.navidrome.org) that provides first-class, fail-closed remote audio streaming directly from the server to Bluetooth speakers, soundbars, and physical audio outputs (via PipeWire and PulseAudio).

---

## 1. Why Navidrome Beam Exists

Historically, playing server-side music from Navidrome to wireless or Bluetooth speakers (such as an Aura Studio, living room soundbar, or workshop speaker) required a complex, multi-container bridge stack:

```text
[Navidrome Web/App] ──> [Home Assistant] ──> [Music Assistant] ──> [Sendspin BT Bridge] ──> [Host BlueZ / PulseAudio] ──> [Bluetooth Speaker]
```

This multi-container architecture caused major reliability and operational challenges:
1. **Architectural Bloat**: Required 3 to 4 separate Docker containers, eating gigabytes of RAM and substantial CPU overhead.
2. **High-Privilege Security Hazards**: Containers like `sendspin-bt-bridge` demanded `network_mode: host`, `cap_add: [NET_ADMIN, NET_RAW]`, `apparmor:unconfined`, and direct passthrough of host physical USB buses (`/dev/bus/usb`).
3. **Sound Leakage & Unwanted Fallback**: When a Bluetooth speaker powered down or went out of range, default PipeWire/WirePlumber fallback logic silently rerouted audio to motherboard PCI/ALSA audio cards (`alsa_output.pci-*`), unexpectedly blasting music in server racks or host rooms.
4. **Web UI Desynchronization**: Closing the browser tab or navigating away often killed server-side playback, while opening a new tab could accidentally overwrite the active server queue with stale browser local storage.

### The Navidrome Beam Architecture

Navidrome Beam eliminates Home Assistant, Music Assistant, and `sendspin-bt-bridge` completely:

```text
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Host Operating System                                                           │
│                                                                                 │
│   ┌──────────────────────────┐         ┌────────────────────────────────────┐   │
│   │ BlueZ D-Bus System Bus   │         │ PipeWire / PulseAudio Audio Server │   │
│   │ /var/run/dbus/system_bus │         │ /run/user/${UID}/pipewire-0        │   │
│   └────────────┬─────────────┘         │ /run/user/${UID}/pulse/native      │   │
│                │                       └─────────────────┬──────────────────┘   │
└────────────────┼─────────────────────────────────────────┼──────────────────────┘
                 │ (bind-mount socket)                     │ (bind-mount sockets)
┌────────────────┼─────────────────────────────────────────┼──────────────────────┐
│ Container: navidrome-beam                                │                      │
│                ▼                                         ▼                      │
│   ┌──────────────────────────┐         ┌────────────────────────────────────┐   │
│   │ BlueZ Device Discovery   │         │ PipeWire Link Supervisor           │   │
│   │ & D-Bus Controller       │         │ (Explicit Stream Pinning)          │   │
│   └────────────┬─────────────┘         └─────────────────▲──────────────────┘   │
│                │                                         │                      │
│                ▼                                         │                      │
│   ┌──────────────────────────┐         ┌─────────────────┴──────────────────┐   │
│   │ Native REST API &        │         │ Headless MPV Playback Subprocess   │   │
│   │ Session Manager          │────────>│ (node.autoconnect=false)           │   │
│   └────────────▲─────────────┘         └─────────────────┬──────────────────┘   │
│                │                                         │                      │
│                │ (/api/jukebox/*, SSE)                   ▼                      │
│   ┌────────────┴─────────────┐         ┌────────────────────────────────────┐   │
│   │ Dual-Track Web UI        │         │ Bluetooth / Hardware Audio Sink    │   │
│   │ (Remote Player Console)  │         │ (Direct Link: Aura Studio, etc.)   │   │
│   └──────────────────────────┘         └────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Core Features & Capabilities

- **Single Container Deployment**: One container (`navidrome-beam`) handles library indexing, the modern Web UI, BlueZ D-Bus discovery, server-side MPV playback, and dynamic PipeWire link orchestration.
- **Fail-Closed Stream Pinning**: Audio nodes are explicitly tagged with `node.autoconnect=false` and `node.dont-reconnect=true`. PipeWire link supervision wires audio stream channels directly to the selected Bluetooth sink. If the speaker drops or powers off, playback halts immediately—**guaranteeing zero audio leakage to motherboard sound cards**.
- **Dual-Track Decoupled Web Console**: In remote playback mode, the browser `<audio>` element, Web Audio API context, and browser streaming network sockets are completely unmounted. The Web UI functions as a responsive remote command console with persistent media hotkeys, volume normalization, and server-side queue control.
- **Resilient Multi-Tab Session Rehydration**: Opening, closing, or refreshing browser tabs synchronizes seamlessly with active server sessions without truncating or overwriting playback queues.
- **Safe Initial Gain**: Default remote volume initializes at safe perceptual loudness curves (15%) to protect ears and speaker hardware from sudden volume bursts.

---

## 3. Quickstart & Deployment

### Prerequisites

1. Linux host with BlueZ and PipeWire (or PulseAudio).
2. The target Bluetooth device paired and trusted on the host:
   ```bash
   bluetoothctl pair 24:C4:06:FA:00:37
   bluetoothctl trust 24:C4:06:FA:00:37
   ```

### Running with Docker Compose

Deploy using the provided `contrib/docker-compose/docker-compose.bluetooth.yml`:

```bash
# Export runtime UID and GID to match the host user running PipeWire
export UID=$(id -u)
export GID=$(id -g)

# Launch Navidrome Beam
docker compose -f contrib/docker-compose/docker-compose.bluetooth.yml up -d
```

### Example `docker-compose.bluetooth.yml`

```yaml
services:
  navidrome:
    image: navidrome-beam:latest
    container_name: navidrome-beam
    user: "${UID:-1000}:${GID:-1000}"
    security_opt:
      - apparmor=unconfined
    volumes:
      - /run/user/${UID:-1000}/pulse/native:/tmp/pulse/native:ro
      - /run/user/${UID:-1000}/pipewire-0:/tmp/pipewire/pipewire-0:ro
      - /var/run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket:ro
      - /dev/snd:/dev/snd
      - ./data:/data
      - ./music:/music:ro
    environment:
      - ND_JUKEBOX_ENABLED=true
      - ND_JUKEBOX_AUTODISCOVERBLUETOOTH=true
      - ND_JUKEBOX_BLUETOOTHMANAGEMENT=true
      - ND_PORT=4534
      - PULSE_SERVER=unix:/tmp/pulse/native
      - PIPEWIRE_RUNTIME_DIR=/tmp/pipewire
      - PIPEWIRE_REMOTE=pipewire-0
    ports:
      - "127.0.0.1:4534:4534"
    group_add:
      - audio
```

---

## 4. Configuration Reference

| Environment Variable | Default | Description |
|----------------------|---------|-------------|
| `ND_JUKEBOX_ENABLED` | `false` | Enables server-side Jukebox playback engine. Must be `true` for Beam. |
| `ND_JUKEBOX_AUTODISCOVERBLUETOOTH` | `false` | Automatically queries BlueZ and audio servers for paired Bluetooth devices. |
| `ND_JUKEBOX_BLUETOOTHMANAGEMENT` | `false` | Exposes device connection and refresh endpoints via Native REST API. |
| `PIPEWIRE_RUNTIME_DIR` | `/tmp/pipewire` | Directory path where the PipeWire socket is mounted inside the container. |
| `PIPEWIRE_REMOTE` | `pipewire-0` | Socket filename for PipeWire IPC communication. |
| `PULSE_SERVER` | `unix:/tmp/pulse/native` | Socket path for fallback PulseAudio client communication. |
| `ND_PORT` | `4533` | HTTP listening port (set to `4534` when running alongside standard instances). |

---

## 5. Security & Runtime Notes

- **AppArmor Profile (`apparmor=unconfined`)**: Required for containerized processes to access host D-Bus system buses without kernel-level method-call blocking.
- **Audio Group (`group_add: [audio]`)**: Grants read/write permissions to `/dev/snd` devices.
- **Socket Permissions**: Sockets in `/run/user/${UID}/` must match the UID of the container user (`user: "${UID}:${GID}"`).

---

## 6. Hardware Compatibility & Verification

Navidrome Beam has been rigorously tested and verified under real-world conditions:
- **Primary Target Device**: Harman Kardon Aura Studio 5 (`24:C4:06:FA:00:37`).
- **Verified Test Cases**:
  - `H01`: Disconnected target sink fail-closed verification (zero PCI audio leak).
  - `H02`: Initial gain safety limit validation (15% loudness curve).
  - `H03`: Paused seek and track scrubbing accuracy via MPV IPC.
  - `H04`: Speaker pause & disconnect state reflection.
  - `H05`: Mid-playback link interruption recovery and fail-closed shutdown.
  - `H06`: Natural queue progression and track EOF transitions.
