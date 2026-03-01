# Bluetooth Auto-Connect and Jukebox Handoff Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
> **Related skills:** @superpowers:test-driven-development, @superpowers:verification-before-completion

**Goal:** Enable Navidrome admins to scan/connect/disconnect Bluetooth audio devices from the Web UI and complete Native API parity for Jukebox handoff controls.

**Architecture:** Add a BlueZ D-Bus service in `core/playback/bluetooth`, expose admin-only Native API endpoints under `/api/bluetooth/*`, and wire the React player UI to those endpoints. Keep existing `/api/jukebox/start` behavior but add `/api/jukebox/play`, `/api/jukebox/pause`, and `/api/jukebox/seek` for explicit control parity while preserving backward compatibility.

**Tech Stack:** Go (`chi`, `godbus/dbus/v5`, `ginkgo/gomega`), React 17 + Redux + Vitest, Docker (Alpine), BlueZ D-Bus.

---

### Task 1: BlueZ D-Bus Core Service

**Files:**
- Create: `core/playback/bluetooth/bluez.go`
- Create: `core/playback/bluetooth/bluez_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Step 1: Write the failing test**

```go
func TestFilterA2DPSinkDevices(t *testing.T) {
	devices := []BlueZDevice{
		{MAC: "24:C4:06:FA:00:37", Name: "Speaker A", UUIDs: []string{"0000110b-0000-1000-8000-00805f9b34fb"}},
		{MAC: "AA:BB:CC:DD:EE:FF", Name: "Keyboard", UUIDs: []string{"00001124-0000-1000-8000-00805f9b34fb"}},
	}
	filtered := filterA2DPSinks(devices)
	if len(filtered) != 1 || filtered[0].MAC != "24:C4:06:FA:00:37" {
		t.Fatalf("expected one A2DP sink, got %#v", filtered)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./core/playback/bluetooth -run TestFilterA2DPSinkDevices -count=1`
Expected: FAIL with `undefined: BlueZDevice` or `undefined: filterA2DPSinks`.

**Step 3: Write minimal implementation**

```go
type BlueZDevice struct {
	Path      dbus.ObjectPath `json:"path"`
	MAC       string          `json:"mac"`
	Name      string          `json:"name"`
	Paired    bool            `json:"paired"`
	Trusted   bool            `json:"trusted"`
	Connected bool            `json:"connected"`
	UUIDs     []string        `json:"uuids"`
}

func filterA2DPSinks(in []BlueZDevice) []BlueZDevice {
	out := make([]BlueZDevice, 0, len(in))
	for _, d := range in {
		if supportsA2DP(d.UUIDs) {
			out = append(out, d)
		}
	}
	return out
}

func supportsA2DP(uuids []string) bool {
	const a2dpSinkUUID = "0000110b-0000-1000-8000-00805f9b34fb"
	for _, u := range uuids {
		if strings.EqualFold(u, a2dpSinkUUID) {
			return true
		}
	}
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./core/playback/bluetooth -run 'Test(FilterA2DPSinkDevices|SupportsA2DP|ListDevicesParsesManagedObjects)' -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add core/playback/bluetooth/bluez.go core/playback/bluetooth/bluez_test.go go.mod go.sum
git commit -m "feat: add bluez dbus bluetooth service core"
```

### Task 2: Add Configuration and UI Feature Flag

**Files:**
- Modify: `conf/configuration.go`
- Modify: `conf/configuration_test.go`
- Modify: `server/serve_index.go`
- Modify: `server/serve_index_test.go`
- Modify: `ui/src/config.js`

**Step 1: Write the failing test**

```go
It("exposes bluetoothManagementEnabled in app config", func() {
	conf.Server.Jukebox.BluetoothManagement = true
	r := httptest.NewRequest("GET", "/index.html", nil)
	w := httptest.NewRecorder()
	serveIndex(ds, fs, nil)(w, r)
	config := extractAppConfig(w.Body.String())
	Expect(config).To(HaveKeyWithValue("bluetoothManagementEnabled", true))
})
```

**Step 2: Run test to verify it fails**

Run: `go test ./server -run TestServer -count=1`
Expected: FAIL with missing `bluetoothManagementEnabled` key.

**Step 3: Write minimal implementation**

```go
type jukeboxOptions struct {
	Enabled               bool
	Devices               []AudioDeviceDefinition
	Default               string
	AdminOnly             bool
	AutoDiscoverBluetooth bool
	BluetoothManagement   bool
}

// defaults
viper.SetDefault("jukebox.bluetoothmanagement", false)
```

```go
appConfig := map[string]any{
	"jukeboxEnabled":             conf.Server.Jukebox.Enabled,
	"bluetoothManagementEnabled": conf.Server.Jukebox.BluetoothManagement,
}
```

```js
const defaultConfig = {
  jukeboxEnabled: false,
  bluetoothManagementEnabled: false,
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./conf ./server -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add conf/configuration.go conf/configuration_test.go server/serve_index.go server/serve_index_test.go ui/src/config.js
git commit -m "feat: add bluetooth management configuration flag"
```

### Task 3: Native API Bluetooth Routes (`/api/bluetooth/*`)

**Files:**
- Create: `server/nativeapi/bluetooth.go`
- Create: `server/nativeapi/bluetooth_test.go`
- Modify: `server/nativeapi/native_api.go`

**Step 1: Write the failing test**

```go
func TestBluetoothRoutes_ListDevices(t *testing.T) {
	// Arrange router with fake bluetooth manager returning one paired speaker
	// GET /api/bluetooth/devices should return 200 and JSON array
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./server/nativeapi -run TestBluetoothRoutes -count=1`
Expected: FAIL with 404 for `/api/bluetooth/devices` or missing handler symbols.

**Step 3: Write minimal implementation**

```go
func (api *Router) addBluetoothRoute(r chi.Router) {
	r.Route("/bluetooth", func(r chi.Router) {
		r.Get("/devices", api.bluetoothDevices)
		r.Post("/scan", api.bluetoothScan)
		r.Post("/connect", api.bluetoothConnect)
		r.Post("/disconnect", api.bluetoothDisconnect)
	})
}

// in routes() admin-only group:
if conf.Server.Jukebox.Enabled && conf.Server.Jukebox.BluetoothManagement {
	api.addBluetoothRoute(r)
}
```

```go
type bluetoothConnectRequest struct {
	MAC string `json:"mac"`
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./server/nativeapi -run TestBluetoothRoutes -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add server/nativeapi/bluetooth.go server/nativeapi/bluetooth_test.go server/nativeapi/native_api.go
git commit -m "feat: add native api bluetooth management endpoints"
```

### Task 4: Complete Jukebox Control Endpoint Parity (`play/pause/seek`)

**Files:**
- Modify: `server/nativeapi/jukebox_control.go`
- Create: `server/nativeapi/jukebox_control_test.go`
- Modify: `ui/src/audioplayer/jukeboxClient.js`

**Step 1: Write the failing test**

```go
func TestJukeboxControlAliases(t *testing.T) {
	// POST /api/jukebox/play should behave as start
	// POST /api/jukebox/pause should pause active track
	// POST /api/jukebox/seek with {"position": 42} should set current track offset
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./server/nativeapi -run TestJukeboxControlAliases -count=1`
Expected: FAIL with 404 on `/api/jukebox/play` and `/api/jukebox/pause`.

**Step 3: Write minimal implementation**

```go
func (api *Router) addJukeboxControlRoute(r chi.Router) {
	r.Get("/jukebox/status", api.jukeboxStatus)
	r.Post("/jukebox/set", api.jukeboxSet)
	r.Post("/jukebox/start", api.jukeboxStart) // backwards compatibility
	r.Post("/jukebox/play", api.jukeboxStart)  // new alias
	r.Post("/jukebox/stop", api.jukeboxStop)
	r.Post("/jukebox/pause", api.jukeboxStop)  // explicit pause endpoint
	r.Post("/jukebox/skip", api.jukeboxSkip)
	r.Post("/jukebox/seek", api.jukeboxSeek)
	r.Post("/jukebox/volume", api.jukeboxVolume)
}
```

```js
const jukeboxClient = {
  play: () => httpClient('/api/jukebox/play', { method: 'POST' }).then(({ json }) => json),
  pause: () => httpClient('/api/jukebox/pause', { method: 'POST' }).then(({ json }) => json),
  seek: (position) =>
    httpClient('/api/jukebox/seek', {
      method: 'POST',
      body: JSON.stringify({ position }),
    }).then(({ json }) => json),
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./server/nativeapi -run TestJukeboxControlAliases -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add server/nativeapi/jukebox_control.go server/nativeapi/jukebox_control_test.go ui/src/audioplayer/jukeboxClient.js
git commit -m "feat: add jukebox play pause seek api parity"
```

### Task 5: DeviceSelector Bluetooth UX (Scan, Connect, Disconnect)

**Files:**
- Create: `ui/src/audioplayer/bluetoothClient.js`
- Modify: `ui/src/audioplayer/DeviceSelector.jsx`
- Create: `ui/src/audioplayer/DeviceSelector.test.jsx`
- Modify: `ui/src/audioplayer/Player.jsx`
- Modify: `ui/src/audioplayer/PlayerToolbar.test.jsx`

**Step 1: Write the failing test**

```jsx
it('shows Scan for devices and triggers /api/bluetooth/scan', async () => {
  // render selector with bluetoothManagementEnabled=true
  // click scan menu item
  // expect httpClient called with POST /api/bluetooth/scan
})
```

**Step 2: Run test to verify it fails**

Run: `npm --prefix ui run test -- ui/src/audioplayer/DeviceSelector.test.jsx`
Expected: FAIL because scan/connect actions do not exist.

**Step 3: Write minimal implementation**

```js
const bluetoothClient = {
  list: () => httpClient('/api/bluetooth/devices').then(({ json }) => json),
  scan: () => httpClient('/api/bluetooth/scan', { method: 'POST' }).then(({ json }) => json),
  connect: (mac) => httpClient('/api/bluetooth/connect', { method: 'POST', body: JSON.stringify({ mac }) }),
  disconnect: (mac) => httpClient('/api/bluetooth/disconnect', { method: 'POST', body: JSON.stringify({ mac }) }),
}
```

```jsx
if (config.bluetoothManagementEnabled) {
  // add "Scan for devices" action
  // render connect/disconnect actions for bluetooth rows
  // on connect success: refresh /api/jukebox/devices and keep jukebox switch flow
}
```

**Step 4: Run test to verify it passes**

Run: `npm --prefix ui run test -- ui/src/audioplayer/DeviceSelector.test.jsx ui/src/audioplayer/PlayerToolbar.test.jsx`
Expected: PASS

**Step 5: Commit**

```bash
git add ui/src/audioplayer/bluetoothClient.js ui/src/audioplayer/DeviceSelector.jsx ui/src/audioplayer/DeviceSelector.test.jsx ui/src/audioplayer/Player.jsx ui/src/audioplayer/PlayerToolbar.test.jsx
git commit -m "feat: add bluetooth scan connect disconnect ui flow"
```

### Task 6: Container Support and Final Verification

**Files:**
- Modify: `contrib/docker-compose/docker-compose.bluetooth.yml`
- Modify: `Dockerfile`
- Modify: `docs/plans/PLAN-bluetooth-auto-connect.md`

**Step 1: Write the failing test/check**

```bash
rg -n "system_bus_socket|DBUS_SYSTEM_BUS_ADDRESS|dbus" contrib/docker-compose/docker-compose.bluetooth.yml Dockerfile
```

Add expectation notes in plan: command must show both `system_bus_socket` mapping and installed `dbus` package in image.

**Step 2: Run check to verify it fails**

Run: `rg -n "system_bus_socket|DBUS_SYSTEM_BUS_ADDRESS|apk add -U --no-cache ffmpeg mpv sqlite pulseaudio-utils dbus" contrib/docker-compose/docker-compose.bluetooth.yml Dockerfile`
Expected: FAIL (missing one or more required entries).

**Step 3: Write minimal implementation**

```yaml
volumes:
  - /var/run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket
environment:
  - DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket
```

```dockerfile
RUN apk add -U --no-cache ffmpeg mpv sqlite pulseaudio-utils dbus
```

**Step 4: Run verification to verify it passes**

Run: `go test ./core/playback/bluetooth ./server/nativeapi -count=1 && npm --prefix ui run test -- ui/src/audioplayer/DeviceSelector.test.jsx`
Expected: PASS

**Step 5: Commit**

```bash
git add contrib/docker-compose/docker-compose.bluetooth.yml Dockerfile docs/plans/PLAN-bluetooth-auto-connect.md
git commit -m "docs: document bluetooth dbus container requirements"
```

## Final Verification Checklist

Run all before merge:

```bash
go test ./core/playback/bluetooth ./server/nativeapi ./conf ./server -count=1
npm --prefix ui run test -- ui/src/audioplayer/DeviceSelector.test.jsx ui/src/audioplayer/PlayerToolbar.test.jsx
```

Expected:
- Go tests: all PASS
- UI tests: all PASS
- No regressions in existing Jukebox device switching behavior

