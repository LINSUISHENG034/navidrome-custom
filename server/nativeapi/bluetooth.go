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

type bluetoothConnectRequest struct {
	MAC string `json:"mac"`
}

func (api *Router) getBluetoothManager() (bluetoothManager, error) {
	if api.bluetoothManager != nil {
		return api.bluetoothManager, nil
	}

	manager, err := bluetooth.NewBlueZManager()
	if err != nil {
		return nil, err
	}
	api.bluetoothManager = manager
	return api.bluetoothManager, nil
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
	manager, err := api.getBluetoothManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := manager.Scan(r.Context(), 10*time.Second); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	api.bluetoothDevices(w, r)
}

func (api *Router) bluetoothConnect(w http.ResponseWriter, r *http.Request) {
	api.bluetoothApplyAction(w, r, func(ctx context.Context, manager bluetoothManager, mac string) error {
		return manager.Connect(ctx, mac)
	})
}

func (api *Router) bluetoothDisconnect(w http.ResponseWriter, r *http.Request) {
	api.bluetoothApplyAction(w, r, func(ctx context.Context, manager bluetoothManager, mac string) error {
		return manager.Disconnect(ctx, mac)
	})
}

func (api *Router) bluetoothApplyAction(w http.ResponseWriter, r *http.Request, fn func(context.Context, bluetoothManager, string) error) {
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

	if err := fn(r.Context(), manager, req.MAC); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if api.playback != nil {
		_ = api.playback.RefreshDevices(r.Context())
	}

	api.bluetoothDevices(w, r)
}
