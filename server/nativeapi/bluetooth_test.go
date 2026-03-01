package nativeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
)

type fakeBluetoothManager struct {
	devices []bluetooth.BlueZDevice
}

func (m *fakeBluetoothManager) ListDevices(context.Context) ([]bluetooth.BlueZDevice, error) {
	return m.devices, nil
}

func (m *fakeBluetoothManager) Scan(context.Context, time.Duration) error {
	return nil
}

func (m *fakeBluetoothManager) Connect(context.Context, string) error {
	return nil
}

func (m *fakeBluetoothManager) Disconnect(context.Context, string) error {
	return nil
}

func TestBluetoothRoutes_ListDevices(t *testing.T) {
	api := &Router{
		bluetoothManager: &fakeBluetoothManager{devices: []bluetooth.BlueZDevice{{
			MAC:    "24:C4:06:FA:00:37",
			Name:   "Speaker A",
			Paired: true,
		}}},
	}

	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		api.addBluetoothRoute(r)
	})

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
