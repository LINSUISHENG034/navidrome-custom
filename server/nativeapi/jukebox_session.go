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
	Takeover   bool   `json:"takeover,omitempty"`
}

// publishJukeboxSessionEvent intentionally uses the request context so the initiating
// client does not receive an echoed SSE event; it already has the authoritative
// HTTP response payload. Other same-user clients still receive the update.
func publishJukeboxSessionEvent(r *http.Request, status playback.SessionStatus) {
	serverevents.GetBroker().SendMessage(r.Context(), playback.NewJukeboxStateUpdatedEvent(status))
}

func (api *Router) addJukeboxSessionRoute(r chi.Router) {
	r.Route("/jukebox/session", func(r chi.Router) {
		r.Post("/attach", api.jukeboxSessionAttach)
		r.Post("/heartbeat", api.jukeboxSessionHeartbeat)
		r.Post("/detach", api.jukeboxSessionDetach)
		r.Post("/handoff", api.jukeboxSessionHandoff)
		r.Get("/status", api.jukeboxSessionStatus)
		r.Get("/snapshot", api.jukeboxSessionSnapshot)
		r.Get("/operation", api.jukeboxOperation)
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
		Takeover:   req.Takeover,
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

func (api *Router) jukeboxSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "sessionId is required", http.StatusBadRequest)
		return
	}

	user, _ := request.UserFrom(r.Context())
	snapshot, err := api.playback.SessionSnapshot(r.Context(), sessionID, user.UserName)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	api.writeJukeboxJSON(w, r, snapshot)
}

func (api *Router) jukeboxSessionHandoff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID    string `json:"sessionId"`
		NewClientID  string `json:"newClientId"`
		HandoffToken string `json:"handoffToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.NewClientID == "" || req.HandoffToken == "" {
		http.Error(w, "sessionId, newClientId, and handoffToken are required", http.StatusBadRequest)
		return
	}

	user, _ := request.UserFrom(r.Context())
	status, err := api.playback.HandoffSession(r.Context(), playback.HandoffRequest{
		SessionID:    req.SessionID,
		User:         user.UserName,
		NewClientID:  req.NewClientID,
		HandoffToken: req.HandoffToken,
	})
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}

	publishJukeboxSessionEvent(r, status)
	api.writeJukeboxJSON(w, r, status)
}

func (api *Router) writeJukeboxSessionError(w http.ResponseWriter, err error) {
	var targetOccupied *playback.TargetOccupiedError
	var historyUnknown *playback.OperationHistoryError
	switch {
	case errors.As(err, &historyUnknown):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "OPERATION_UNKNOWN", "status": playback.OperationStatusUnknown,
			"error": err.Error(), "bindingGeneration": historyUnknown.BindingGeneration,
			"requiresNewGeneration": true,
		})
	case errors.Is(err, playback.ErrOperationUnknown):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "OPERATION_UNKNOWN", "status": playback.OperationStatusUnknown, "error": err.Error(),
		})
	case errors.Is(err, playback.ErrOperationRequired):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OPERATION_ID_REQUIRED", "error": err.Error()})
	case errors.Is(err, playback.ErrOperationIdentity):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OPERATION_ID_INVALID", "error": err.Error()})
	case errors.Is(err, playback.ErrOperationCapacity):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "OPERATION_CAPACITY", "error": err.Error()})
	case errors.As(err, &targetOccupied):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":             "TARGET_OCCUPIED",
			"error":            targetOccupied.Error(),
			"sceneFingerprint": targetOccupied.SceneFingerprint,
			"queueCount":       targetOccupied.QueueCount,
		})
	case errors.Is(err, playback.ErrGenerationRequired):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "GENERATION_REQUIRED",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrCommandSuperseded):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "COMMAND_SUPERSEDED",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrMigrationConflict):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "MIGRATION_CONFLICT",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrHandoffInvalid):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "HANDOFF_INVALID",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrReservationNotFound):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "RESERVATION_NOT_FOUND",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrSessionNotFound), errors.Is(err, playback.ErrDeviceNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, playback.ErrSessionOwnership):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":  "SESSION_OWNERSHIP",
			"error": err.Error(),
		})
	case errors.Is(err, playback.ErrCommandScope):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
