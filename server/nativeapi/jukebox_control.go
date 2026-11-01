package nativeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
)

type jukeboxStatusResponse struct {
	CurrentIndex  int     `json:"currentIndex"`
	Playing       bool    `json:"playing"`
	Gain          float32 `json:"gain"`
	DesiredGain   float32 `json:"desiredGain"`
	Muted         bool    `json:"muted"`
	Position      int     `json:"position"`
	QueueVersion  int     `json:"queueVersion"`
	StateSequence uint64  `json:"stateSequence"`
	// QueueProtocol is the shared additive capability contract; clients must
	// check version/boolean values, never field presence.
	QueueProtocol *model.QueueProtocolCapabilities `json:"queueProtocol,omitempty"`
}

type jukeboxCommandRequest struct {
	playback.CommandScope
	IDs                  []string `json:"ids"`
	Index                *int     `json:"index"`
	Offset               int      `json:"offset"`
	Position             int      `json:"position"`
	Gain                 float32  `json:"gain"`
	Muted                *bool    `json:"muted"`
	From                 *int     `json:"from"`
	To                   *int     `json:"to"`
	QueueItemID          string   `json:"queueItemId"`
	AnchorQueueItemID    string   `json:"anchorQueueItemId"`
	Placement            string   `json:"placement"`
	ExpectedQueueVersion *int     `json:"expectedQueueVersion"`
}

func (api *Router) addJukeboxControlRoute(r chi.Router) {
	r.Get("/jukebox/status", api.jukeboxStatus)
	r.Get("/jukebox/operation", api.jukeboxOperation)
	r.Post("/jukebox/migration/reserve", api.jukeboxMigrationReserve)
	r.Post("/jukebox/migration/release", api.jukeboxMigrationRelease)
	r.Post("/jukebox/migration/commit", api.jukeboxMigrationCommit)
	r.Get("/jukebox/migration/reservation", api.jukeboxMigrationReservation)
	r.Post("/jukebox/set", api.jukeboxSet)
	r.Post("/jukebox/start", api.jukeboxStart)
	r.Post("/jukebox/play", api.jukeboxStart)
	r.Post("/jukebox/stop", api.jukeboxShutdown)
	r.Post("/jukebox/pause", api.jukeboxStop)
	r.Post("/jukebox/skip", api.jukeboxSkip)
	r.Post("/jukebox/seek", api.jukeboxSeek)
	r.Post("/jukebox/volume", api.jukeboxVolume)
	r.Post("/jukebox/mute", api.jukeboxMute)
	r.Post("/jukebox/add", api.jukeboxAdd)
	r.Post("/jukebox/remove", api.jukeboxRemove)
	r.Post("/jukebox/move", api.jukeboxMove)
}

func toStatusResponse(s playback.DeviceStatus) *jukeboxStatusResponse {
	return &jukeboxStatusResponse{
		CurrentIndex:  s.CurrentIndex,
		Playing:       s.Playing,
		Gain:          s.Gain,
		DesiredGain:   s.DesiredGain,
		Muted:         s.Muted,
		Position:      s.Position,
		QueueVersion:  s.QueueVersion,
		StateSequence: s.StateSequence,
		QueueProtocol: queueProtocolPtr(),
	}
}

func queueProtocolPtr() *model.QueueProtocolCapabilities {
	protocol := playback.CurrentQueueProtocol()
	return &protocol
}

func writeJukeboxMoveProtocolConflict(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": "MOVE_PROTOCOL_CONFLICT", "error": err.Error()})
}

func writeJukeboxIdentityConflict(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": "QUEUE_IDENTITY_CONFLICT", "error": err.Error()})
}

// validateJukeboxMoveProtocol enforces the relative-anchor move contract
// before any authority check: anchor fields cannot be combined with any
// legacy from/to/toIndex field, and a partial anchor request is rejected.
func validateJukeboxMoveProtocol(req *jukeboxCommandRequest) error {
	anchorFields := req.QueueItemID != "" || req.AnchorQueueItemID != "" || req.Placement != "" || req.ExpectedQueueVersion != nil
	if !anchorFields {
		return nil
	}
	if req.From != nil || req.To != nil || req.Index != nil {
		return playback.ErrMoveProtocolConflict
	}
	if req.QueueItemID == "" || req.AnchorQueueItemID == "" ||
		(req.Placement != playback.MovePlacementBefore && req.Placement != playback.MovePlacementAfter) {
		return fmt.Errorf("queueItemId, anchorQueueItemId, and placement (before|after) are required for relative moves")
	}
	return nil
}

func (api *Router) writeJukeboxJSON(w http.ResponseWriter, r *http.Request, resp any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error(r.Context(), "Error encoding jukebox status", err)
	}
}

func (api *Router) jukeboxStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := request.UserFrom(r.Context())
	if sessionID := r.URL.Query().Get("sessionId"); sessionID != "" {
		snapshot, err := api.playback.SessionSnapshot(r.Context(), sessionID, user.UserName)
		if err != nil {
			api.writeJukeboxSessionError(w, err)
			return
		}
		status := snapshot.SessionStatus
		api.writeJukeboxJSON(w, r, toStatusResponse(playback.DeviceStatus{
			CurrentIndex: status.CurrentIndex, Playing: status.Playing, Position: status.Position,
			Gain: status.Gain, DesiredGain: status.DesiredGain, Muted: status.Muted, QueueVersion: status.QueueVersion,
			StateSequence: status.StateSequence,
		}))
		return
	}
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

func commandScopeFromRequest(r *http.Request, scope playback.CommandScope) (playback.CommandScope, error) {
	query := r.URL.Query()
	querySession, queryClient := query.Get("sessionId"), query.Get("clientId")
	if (scope.SessionID != "" && querySession != "" && scope.SessionID != querySession) ||
		(scope.ClientID != "" && queryClient != "" && scope.ClientID != queryClient) {
		return playback.CommandScope{}, playback.ErrCommandScope
	}
	if scope.SessionID == "" {
		scope.SessionID = querySession
	}
	if scope.ClientID == "" {
		scope.ClientID = queryClient
	}
	if scope.BindingGeneration == nil {
		if qGen := query.Get("bindingGeneration"); qGen != "" {
			if val, err := strconv.ParseUint(qGen, 10, 64); err == nil {
				scope.BindingGeneration = &val
			} else {
				return playback.CommandScope{}, playback.ErrGenerationRequired
			}
		}
	}
	if scope.OperationID == "" {
		scope.OperationID = query.Get("operationId")
	}
	user, _ := request.UserFrom(r.Context())
	scope.User = user.UserName
	return scope, nil
}

func (api *Router) jukeboxCommand(w http.ResponseWriter, r *http.Request, action string, bodyRequired bool) {
	var req jukeboxCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && (bodyRequired || !errors.Is(err, io.EOF)) {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	scope, err := commandScopeFromRequest(r, req.CommandScope)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	if action == playback.CommandMute && req.Muted == nil {
		http.Error(w, "muted boolean is required", http.StatusBadRequest)
		return
	}
	if action == playback.CommandSeek && req.Position < 0 {
		http.Error(w, "position must be >= 0", http.StatusBadRequest)
		return
	}
	if action == playback.CommandMove {
		if err := validateJukeboxMoveProtocol(&req); err != nil {
			if errors.Is(err, playback.ErrMoveProtocolConflict) {
				writeJukeboxMoveProtocolConflict(w, err)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if (action == playback.CommandRemove || action == playback.CommandSkip) && req.QueueItemID != "" && req.Index != nil {
		writeJukeboxIdentityConflict(w, playback.ErrQueueIdentityConflict)
		return
	}
	command := playback.PlaybackCommand{
		Action: action, IDs: req.IDs, Offset: req.Offset, Position: req.Position, Gain: req.Gain,
		QueueItemID: req.QueueItemID, AnchorQueueItemID: req.AnchorQueueItemID, Placement: req.Placement,
		ExpectedQueueVersion: req.ExpectedQueueVersion, IndexProvided: req.Index != nil,
	}
	if req.From != nil {
		command.From = *req.From
	}
	if req.To != nil {
		command.To = *req.To
	}
	if req.Muted != nil {
		command.Muted = *req.Muted
	}
	if req.Index != nil {
		command.Index = *req.Index
		if action == playback.CommandAdd {
			command.Action = playback.CommandInsert
		}
	}
	status, err := api.playback.ExecuteCommand(r.Context(), scope, command)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	api.writeJukeboxJSON(w, r, toStatusResponse(status))
}

func (api *Router) jukeboxSet(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandSet, true)
}

func (api *Router) jukeboxStart(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandStart, false)
}

func (api *Router) jukeboxStop(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandPause, false)
}

func (api *Router) jukeboxShutdown(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandStop, false)
}

func (api *Router) jukeboxSkip(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandSkip, true)
}

func (api *Router) jukeboxVolume(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandGain, true)
}

func (api *Router) jukeboxMute(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandMute, true)
}

func (api *Router) jukeboxSeek(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandSeek, true)
}

func (api *Router) jukeboxAdd(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandAdd, true)
}

func (api *Router) jukeboxRemove(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandRemove, true)
}

func (api *Router) jukeboxMove(w http.ResponseWriter, r *http.Request) {
	api.jukeboxCommand(w, r, playback.CommandMove, true)
}

func (api *Router) jukeboxOperation(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	operationID := r.URL.Query().Get("operationId")
	if sessionID == "" || operationID == "" {
		http.Error(w, "sessionId and operationId are required", http.StatusBadRequest)
		return
	}
	rec, err := api.playback.QueryOperation(r.Context(), sessionID, operationID)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	api.writeJukeboxJSON(w, r, rec)
}

func (api *Router) jukeboxMigrationReserve(w http.ResponseWriter, r *http.Request) {
	var req playback.ReserveMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.MigrationID == "" || req.DeviceName == "" {
		http.Error(w, "migrationId and deviceName are required", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	req.User = user.UserName
	res, err := api.playback.ReserveMigration(r.Context(), req)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	api.writeJukeboxJSON(w, r, res)
}

func (api *Router) jukeboxMigrationRelease(w http.ResponseWriter, r *http.Request) {
	var req playback.ReleaseMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.MigrationID == "" {
		http.Error(w, "migrationId is required", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	req.User = user.UserName
	res, err := api.playback.ReleaseMigrationReservation(r.Context(), req)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	api.writeJukeboxJSON(w, r, res)
}

func (api *Router) jukeboxMigrationReservation(w http.ResponseWriter, r *http.Request) {
	migrationID := r.URL.Query().Get("migrationId")
	if migrationID == "" {
		http.Error(w, "migrationId is required", http.StatusBadRequest)
		return
	}
	info, err := api.playback.QueryReservation(r.Context(), migrationID)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	api.writeJukeboxJSON(w, r, info)
}

func (api *Router) jukeboxMigrationCommit(w http.ResponseWriter, r *http.Request) {
	var req playback.CommitMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.MigrationID == "" || req.SessionID == "" || req.TargetDeviceName == "" {
		http.Error(w, "migrationId, sessionId, and targetDeviceName are required", http.StatusBadRequest)
		return
	}
	user, _ := request.UserFrom(r.Context())
	req.User = user.UserName
	status, err := api.playback.CommitMigration(r.Context(), req)
	if err != nil {
		api.writeJukeboxSessionError(w, err)
		return
	}
	publishJukeboxSessionEvent(r, status)
	api.writeJukeboxJSON(w, r, status)
}
