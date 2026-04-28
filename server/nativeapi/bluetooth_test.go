package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
)

type fakeBluetoothManager struct {
	devices    []bluetooth.BlueZDevice
	connectMAC string
	scanDelay  time.Duration
	listErr    error
	scanErr    error
	closed     bool
	scanCalls  int
}

func (m *fakeBluetoothManager) ListDevices(context.Context) ([]bluetooth.BlueZDevice, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.devices, nil
}

func (m *fakeBluetoothManager) Scan(ctx context.Context, _ time.Duration) error {
	m.scanCalls++
	if m.scanDelay > 0 {
		select {
		case <-time.After(m.scanDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.scanErr
}

func (m *fakeBluetoothManager) Connect(_ context.Context, mac string) error {
	m.connectMAC = mac
	return nil
}

func (m *fakeBluetoothManager) Disconnect(context.Context, string) error {
	return nil
}

func (m *fakeBluetoothManager) Close() error {
	m.closed = true
	return nil
}

func bluetoothTestRouter(api *Router) http.Handler {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		api.addBluetoothRoute(r)
	})
	return r
}

func TestBluetoothRoutes_ListDevices(t *testing.T) {
	api := &Router{
		bluetoothManager: &fakeBluetoothManager{devices: []bluetooth.BlueZDevice{{
			MAC:    "24:C4:06:FA:00:37",
			Name:   "Speaker A",
			Paired: true,
		}}},
	}

	r := bluetoothTestRouter(api)

	req := httptest.NewRequest(http.MethodGet, "/api/bluetooth/devices", nil)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}

	var devices []bluetooth.BlueZDevice
	if err := json.Unmarshal(res.Body.Bytes(), &devices); err != nil {
		t.Fatalf("invalid response json: %v", err)
	}
	if len(devices) != 1 || devices[0].MAC != "24:C4:06:FA:00:37" {
		t.Fatalf("unexpected response devices: %#v", devices)
	}
}

func TestBluetoothRoutes_ConnectWaitsForSinkReadiness(t *testing.T) {
	manager := &fakeBluetoothManager{devices: []bluetooth.BlueZDevice{{
		MAC:       "24:C4:06:FA:00:37",
		Name:      "Speaker A",
		Connected: true,
	}}}
	api := &Router{bluetoothManager: manager}

	previousWait := waitForBluetoothSinkReady
	defer func() { waitForBluetoothSinkReady = previousWait }()

	var waitedMAC string
	waitForBluetoothSinkReady = func(_ context.Context, mac string, _ time.Duration, _ time.Duration) (bluetooth.AudioSink, bool) {
		waitedMAC = mac
		return bluetooth.AudioSink{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"}, true
	}

	req := httptest.NewRequest(http.MethodPost, "/api/bluetooth/connect", strings.NewReader(`{"mac":"24:C4:06:FA:00:37"}`))
	res := httptest.NewRecorder()
	bluetoothTestRouter(api).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}
	if manager.connectMAC != "24:C4:06:FA:00:37" {
		t.Fatalf("Connect called with %q", manager.connectMAC)
	}
	if waitedMAC != "24:C4:06:FA:00:37" {
		t.Fatalf("wait called with %q", waitedMAC)
	}

	var body bluetoothActionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid response json: %v", err)
	}
	if body.Warning != nil {
		t.Fatalf("unexpected warning: %#v", body.Warning)
	}
	if len(body.Devices) != 1 {
		t.Fatalf("unexpected response devices: %#v", body.Devices)
	}
}

func TestBluetoothRoutes_ConnectReturnsSinkNotReadyWarning(t *testing.T) {
	manager := &fakeBluetoothManager{devices: []bluetooth.BlueZDevice{{
		MAC:       "24:C4:06:FA:00:37",
		Name:      "Speaker A",
		Connected: true,
	}}}
	api := &Router{bluetoothManager: manager}

	previousWait := waitForBluetoothSinkReady
	defer func() { waitForBluetoothSinkReady = previousWait }()
	waitForBluetoothSinkReady = func(_ context.Context, _ string, _ time.Duration, _ time.Duration) (bluetooth.AudioSink, bool) {
		return bluetooth.AudioSink{}, false
	}

	req := httptest.NewRequest(http.MethodPost, "/api/bluetooth/connect", strings.NewReader(`{"mac":"24:C4:06:FA:00:37"}`))
	res := httptest.NewRecorder()
	bluetoothTestRouter(api).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body.String())
	}

	var body bluetoothActionResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid response json: %v", err)
	}
	if body.Warning == nil {
		t.Fatalf("expected warning, got nil")
	}
	if body.Warning.Code != "sink_not_ready" {
		t.Fatalf("warning code = %q", body.Warning.Code)
	}
}

func TestBluetoothRoutes_ResetsManagerOnUnavailableError(t *testing.T) {
	manager := &fakeBluetoothManager{listErr: errors.New("dbus: connection closed by user")}
	api := &Router{bluetoothManager: manager}

	req := httptest.NewRequest(http.MethodGet, "/api/bluetooth/devices", nil)
	res := httptest.NewRecorder()
	bluetoothTestRouter(api).ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", res.Code)
	}
	if api.bluetoothManager != nil {
		t.Fatalf("expected bluetooth manager reset, got %#v", api.bluetoothManager)
	}
	if !manager.closed {
		t.Fatal("expected manager to be closed")
	}
}

func TestBluetoothRoutes_GetBluetoothManagerIsLazyAndCached(t *testing.T) {
	previousFactory := newBluetoothManager
	defer func() { newBluetoothManager = previousFactory }()

	calls := 0
	manager := &fakeBluetoothManager{}
	newBluetoothManager = func() (bluetoothManager, error) {
		calls++
		return manager, nil
	}

	api := &Router{}
	first, err := api.getBluetoothManager()
	if err != nil {
		t.Fatalf("first getBluetoothManager: %v", err)
	}
	second, err := api.getBluetoothManager()
	if err != nil {
		t.Fatalf("second getBluetoothManager: %v", err)
	}
	if first != manager || second != manager {
		t.Fatalf("unexpected managers: %#v %#v", first, second)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
}
