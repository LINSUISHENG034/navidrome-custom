package nativeapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/model/request"
	serverevents "github.com/navidrome/navidrome/server/events"
)

type jukeboxSessionRequest struct {
	SessionID  string `json:"sessionId"`
	ClientID   string `json:"clientId"`
	DeviceName string `json:"deviceName,omitempty"`
}

func publishJukeboxSessionEvent(r *http.Request, status playback.SessionStatus) {
	serverevents.GetBroker().SendMessage(r.Context(), &serverevents.JukeboxStateUpdated{
		SessionID:     status.SessionID,
		DeviceName:    status.DeviceName,
		OwnerClientID: status.OwnerClientID,
		CurrentIndex:  status.CurrentIndex,
		TrackID:       status.TrackID,
		Playing:       status.Playing,
		Position:      status.Position,
		Gain:          status.Gain,
		Attached:      status.Attached,
		QueueVersion:  status.QueueVersion,
		LastHeartbeat: status.LastHeartbeat,
	})
}

func (api *Router) addJukeboxSessionRoute(r chi.Router) {
	r.Route("/jukebox/session", func(r chi.Router) {
		r.Post("/attach", api.jukeboxSessionAttach)
		r.Post("/heartbeat", api.jukeboxSessionHeartbeat)
		r.Post("/detach", api.jukeboxSessionDetach)
		r.Get("/status", api.jukeboxSessionStatus)
	})
}

func (api *Router) jukeboxSessionAttach(w http.ResponseWriter, r *http.Request) {
	var req jukeboxSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.ClientID == "" {
		http.Error(w, "sessionId and clientId are required", http.StatusBadRequest)
		return
	}

	user, _ := request.UserFrom(r.Context())
	status, err := api.playback.AttachSession(r.Context(), playback.AttachRequest{
		SessionID:  req.SessionID,
		User:       user.UserName,
		ClientID:   req.ClientID,
		DeviceName: req.DeviceName,
	})
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	publishJukeboxSessionEvent(r, status)
	api.writeJukeboxJSON(w, r, status)
}

func (api *Router) jukeboxSessionHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req jukeboxSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.ClientID == "" {
		http.Error(w, "sessionId and clientId are required", http.StatusBadRequest)
		return
	}

	status, err := api.playback.HeartbeatSession(r.Context(), req.SessionID, req.ClientID)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	publishJukeboxSessionEvent(r, status)
	api.writeJukeboxJSON(w, r, status)
}

func (api *Router) jukeboxSessionDetach(w http.ResponseWriter, r *http.Request) {
	var req jukeboxSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.ClientID == "" {
		http.Error(w, "sessionId and clientId are required", http.StatusBadRequest)
		return
	}

	status, err := api.playback.DetachSession(r.Context(), req.SessionID, req.ClientID)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	publishJukeboxSessionEvent(r, status)
	api.writeJukeboxJSON(w, r, status)
}

func (api *Router) jukeboxSessionStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "sessionId is required", http.StatusBadRequest)
		return
	}

	status, err := api.playback.SessionStatus(r.Context(), sessionID)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	api.writeJukeboxJSON(w, r, status)
}

func (api *Router) writeJukeboxSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, playback.ErrSessionNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, playback.ErrSessionOwnership):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
