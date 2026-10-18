package nativeapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/log"
)

func (api *Router) addJukeboxDeviceRoute(r chi.Router) {
	r.Route("/jukebox/devices", func(r chi.Router) {
		r.Get("/", api.listDevices)
		r.Post("/switch", api.switchDevice)
		r.Post("/refresh", api.refreshDevices)
	})
}

func (api *Router) listDevices(w http.ResponseWriter, r *http.Request) {
	devices := api.playback.ListDevices()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(devices); err != nil {
		log.Error(r.Context(), "Error encoding device list", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (api *Router) switchDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		playback.CommandScope
		DeviceName string `json:"deviceName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DeviceName == "" {
		http.Error(w, "deviceName is required", http.StatusBadRequest)
		return
	}

	scope, err := commandScopeFromRequest(r, req.CommandScope)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	if err := api.playback.SwitchDeviceForCommand(r.Context(), scope, req.DeviceName); err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	api.listDevices(w, r)
}

func (api *Router) refreshDevices(w http.ResponseWriter, r *http.Request) {
	if err := api.playback.RefreshDevices(r.Context()); err != nil {
		log.Error(r.Context(), "Error refreshing devices", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	api.listDevices(w, r)
}
