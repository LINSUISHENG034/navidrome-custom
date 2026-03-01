package nativeapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/request"
)

type jukeboxStatusResponse struct {
	CurrentIndex int     `json:"currentIndex"`
	Playing      bool    `json:"playing"`
	Gain         float32 `json:"gain"`
	Position     int     `json:"position"`
}

func (api *Router) addJukeboxControlRoute(r chi.Router) {
	r.Get("/jukebox/status", api.jukeboxStatus)
	r.Post("/jukebox/set", api.jukeboxSet)
	r.Post("/jukebox/start", api.jukeboxStart)
	r.Post("/jukebox/stop", api.jukeboxStop)
	r.Post("/jukebox/skip", api.jukeboxSkip)
	r.Post("/jukebox/volume", api.jukeboxVolume)
}

func toStatusResponse(s playback.DeviceStatus) *jukeboxStatusResponse {
	return &jukeboxStatusResponse{
		CurrentIndex: s.CurrentIndex,
		Playing:      s.Playing,
		Gain:         s.Gain,
		Position:     s.Position,
	}
}

func (api *Router) writeJukeboxJSON(w http.ResponseWriter, r *http.Request, resp *jukeboxStatusResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error(r.Context(), "Error encoding jukebox status", err)
	}
}

func (api *Router) jukeboxStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Status(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Set(r.Context(), req.IDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxStart(w http.ResponseWriter, r *http.Request) {
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Start(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxStop(w http.ResponseWriter, r *http.Request) {
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Stop(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxSkip(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index  int `json:"index"`
		Offset int `json:"offset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Skip(r.Context(), req.Index, req.Offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gain float32 `json:"gain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.SetGain(r.Context(), req.Gain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}
