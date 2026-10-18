package nativeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
	"github.com/navidrome/navidrome/log"
)

type bluetoothManager interface {
	ListDevices(ctx context.Context) ([]bluetooth.BlueZDevice, error)
	Scan(ctx context.Context, timeout time.Duration) error
	Connect(ctx context.Context, mac string) error
	Disconnect(ctx context.Context, mac string) error
}

type bluetoothManagerCloser interface {
	Close() error
}

type bluetoothConnectRequest struct {
	MAC string `json:"mac"`
}

type bluetoothActionWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bluetoothActionResponse struct {
	Devices []bluetooth.BlueZDevice `json:"devices"`
	Warning *bluetoothActionWarning `json:"warning,omitempty"`
}

var (
	waitForBluetoothSinkReady = bluetooth.WaitForSinkReady
	newBluetoothManager       = func() (bluetoothManager, error) {
		return bluetooth.NewBlueZManager()
	}
)

func (api *Router) getBluetoothManager() (bluetoothManager, error) {
	api.bluetoothMu.Lock()
	defer api.bluetoothMu.Unlock()

	if api.bluetoothManager != nil {
		return api.bluetoothManager, nil
	}

	manager, err := newBluetoothManager()
	if err != nil {
		return nil, err
	}
	api.bluetoothManager = manager
	return api.bluetoothManager, nil
}

func (api *Router) resetBluetoothManager(ctx context.Context, manager bluetoothManager, err error) {
	if !bluetooth.IsUnavailableError(err) {
		return
	}

	api.bluetoothMu.Lock()
	defer api.bluetoothMu.Unlock()
	if api.bluetoothManager != manager {
		return
	}

	if closer, ok := api.bluetoothManager.(bluetoothManagerCloser); ok {
		if closeErr := closer.Close(); closeErr != nil {
			log.Warn(ctx, "Error closing bluetooth manager after D-Bus failure", "err", closeErr)
		}
	}
	api.bluetoothManager = nil
	log.Warn(ctx, "Reset bluetooth manager after D-Bus failure", "err", err)
}

func (api *Router) addBluetoothRoute(r chi.Router) {
	r.Route("/bluetooth", func(r chi.Router) {
		r.Get("/devices", api.bluetoothDevices)
		r.Post("/scan", api.bluetoothScan)
		r.Post("/connect", api.bluetoothConnect)
		r.Post("/disconnect", api.bluetoothDisconnect)
	})
}

func (api *Router) bluetoothDevices(w http.ResponseWriter, r *http.Request) {
	manager, err := api.getBluetoothManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	devices, err := manager.ListDevices(r.Context())
	if err != nil {
		api.resetBluetoothManager(r.Context(), manager, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(devices); err != nil {
		log.Error(r.Context(), "Error encoding bluetooth device list", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (api *Router) bluetoothScan(w http.ResponseWriter, r *http.Request) {
	api.bluetoothScanMu.Lock()
	defer api.bluetoothScanMu.Unlock()

	manager, err := api.getBluetoothManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := manager.Scan(r.Context(), 10*time.Second); err != nil {
		api.resetBluetoothManager(r.Context(), manager, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	api.bluetoothDevices(w, r)
}

func (api *Router) bluetoothConnect(w http.ResponseWriter, r *http.Request) {
	api.bluetoothApplyAction(w, r, func(ctx context.Context, manager bluetoothManager, mac string) (*bluetoothActionWarning, error) {
		if err := manager.Connect(ctx, mac); err != nil {
			return nil, err
		}

		started := time.Now()
		sink, ready := waitForBluetoothSinkReady(ctx, mac, 3*time.Second, 200*time.Millisecond)
		if ready {
			log.Debug(ctx, "Bluetooth audio sink ready after connect", "mac", bluetooth.NormalizeMAC(mac), "sink", sink.Name, "elapsed", time.Since(started))
			return nil, nil
		}

		elapsed := time.Since(started)
		log.Warn(ctx, "Bluetooth audio sink not ready after connect", "mac", bluetooth.NormalizeMAC(mac), "elapsed", elapsed)
		return &bluetoothActionWarning{
			Code:    "sink_not_ready",
			Message: "Bluetooth device connected, but its audio output is still appearing.",
		}, nil
	})
}

func (api *Router) bluetoothDisconnect(w http.ResponseWriter, r *http.Request) {
	api.bluetoothApplyAction(w, r, func(ctx context.Context, manager bluetoothManager, mac string) (*bluetoothActionWarning, error) {
		return nil, manager.Disconnect(ctx, mac)
	})
}

func (api *Router) bluetoothApplyAction(w http.ResponseWriter, r *http.Request, fn func(context.Context, bluetoothManager, string) (*bluetoothActionWarning, error)) {
	manager, err := api.getBluetoothManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	var req bluetoothConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.MAC == "" {
		http.Error(w, "mac is required", http.StatusBadRequest)
		return
	}

	warning, err := fn(r.Context(), manager, req.MAC)
	if err != nil {
		api.resetBluetoothManager(r.Context(), manager, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if api.playback != nil {
		_ = api.playback.RefreshDevices(r.Context())
	}

	devices, err := manager.ListDevices(r.Context())
	if err != nil {
		api.resetBluetoothManager(r.Context(), manager, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(bluetoothActionResponse{Devices: devices, Warning: warning}); err != nil {
		log.Error(r.Context(), "Error encoding bluetooth action response", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
