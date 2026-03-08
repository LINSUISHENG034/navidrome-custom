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
	r.Post("/jukebox/play", api.jukeboxStart)
	r.Post("/jukebox/stop", api.jukeboxShutdown)
	r.Post("/jukebox/pause", api.jukeboxStop)
	r.Post("/jukebox/skip", api.jukeboxSkip)
	r.Post("/jukebox/seek", api.jukeboxSeek)
	r.Post("/jukebox/volume", api.jukeboxVolume)
	r.Post("/jukebox/add", api.jukeboxAdd)
	r.Post("/jukebox/remove", api.jukeboxRemove)
	r.Post("/jukebox/move", api.jukeboxMove)
}

func toStatusResponse(s playback.DeviceStatus) *jukeboxStatusResponse {
	return &jukeboxStatusResponse{
		CurrentIndex: s.CurrentIndex,
		Playing:      s.Playing,
		Gain:         s.Gain,
		Position:     s.Position,
	}
}

func (api *Router) writeJukeboxJSON(w http.ResponseWriter, r *http.Request, resp any) {
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

func (api *Router) jukeboxShutdown(w http.ResponseWriter, r *http.Request) {
	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Shutdown(r.Context())
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

func (api *Router) jukeboxSeek(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Position int `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Position < 0 {
		http.Error(w, "position must be >= 0", http.StatusBadRequest)
		return
	}

	user, _ := request.UserFrom(r.Context())
	pb, err := api.playback.GetDeviceForUser(user.UserName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, err := pb.Status(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := pb.Skip(r.Context(), current.CurrentIndex, req.Position)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids"`
		Index *int     `json:"index,omitempty"`
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

	var status playback.DeviceStatus
	if req.Index != nil {
		status, err = pb.Insert(r.Context(), *req.Index, req.IDs)
	} else {
		status, err = pb.Add(r.Context(), req.IDs)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
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
	status, err := pb.Remove(r.Context(), req.Index)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From int `json:"from"`
		To   int `json:"to"`
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
	status, err := pb.Move(r.Context(), req.From, req.To)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}
