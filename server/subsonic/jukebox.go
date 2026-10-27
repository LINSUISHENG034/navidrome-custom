package subsonic

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/slice"
)

const (
	ActionGet     = "get"
	ActionStatus  = "status"
	ActionSet     = "set"
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionSkip    = "skip"
	ActionAdd     = "add"
	ActionClear   = "clear"
	ActionRemove  = "remove"
	ActionShuffle = "shuffle"
	ActionSetGain = "setGain"
)

func (api *Router) JukeboxControl(r *http.Request) (*responses.Subsonic, error) {
	ctx := r.Context()
	user := getUser(ctx)
	p := req.Params(r)

	if !conf.Server.Jukebox.Enabled {
		return nil, newError(responses.ErrorGeneric, "Jukebox is disabled")
	}
	if conf.Server.Jukebox.AdminOnly && !user.IsAdmin {
		return nil, newError(responses.ErrorAuthorizationFail, "Jukebox is admin only")
	}
	actionString, err := p.String("action")
	if err != nil {
		return nil, err
	}
	log.Info(ctx, "JukeboxControl request received", "action", actionString)

	if actionString == ActionGet || actionString == ActionStatus {
		pb, err := api.playback.GetDeviceForUser(user.UserName)
		if err != nil {
			return nil, err
		}
		if actionString == ActionStatus {
			return createResponse(pb.Status(ctx))
		}
		mediafiles, status, err := pb.Get(ctx)
		if err != nil {
			return nil, err
		}
		playlist := responses.JukeboxPlaylist{
			JukeboxStatus: *deviceStatusToJukeboxStatus(status),
			Entry:         slice.MapWithArg(mediafiles, ctx, childFromMediaFile),
		}
		response := newResponse()
		response.JukeboxPlaylist = &playlist
		return response, nil
	}

	command := playback.PlaybackCommand{}
	switch actionString {
	case ActionSet:
		command.Action, command.IDs = playback.CommandSet, p.Strings("id")
	case ActionStart:
		command.Action = playback.CommandStart
	case ActionStop:
		// The Subsonic stop action historically means resumable pause.
		command.Action = playback.CommandPause
	case ActionSkip:
		command.Action = playback.CommandSkip
		command.Index, err = p.Int("index")
		if err != nil {
			return nil, newError(responses.ErrorMissingParameter, "missing parameter index, err: %s", err)
		}
		command.Offset = p.IntOr("offset", 0)
	case ActionAdd:
		command.Action, command.IDs = playback.CommandAdd, p.Strings("id")
	case ActionClear:
		command.Action = playback.CommandClear
	case ActionRemove:
		command.Action = playback.CommandRemove
		command.Index, err = p.Int("index")
		if err != nil {
			return nil, err
		}
	case ActionShuffle:
		command.Action = playback.CommandShuffle
	case ActionSetGain:
		gainStr, err := p.String("gain")
		if err != nil {
			return nil, newError(responses.ErrorMissingParameter, "missing parameter gain, err: %s", err)
		}
		gain, err := strconv.ParseFloat(gainStr, 32)
		if err != nil {
			return nil, newError(responses.ErrorMissingParameter, "error parsing gain float value, err: %s", err)
		}
		command.Action, command.Gain = playback.CommandGain, float32(gain)
	default:
		return nil, newError(responses.ErrorMissingParameter, "Unknown action: %s", actionString)
	}
	var bindingGeneration *uint64
	if encodedGeneration := p.StringOr("bindingGeneration", ""); encodedGeneration != "" {
		generation, parseErr := strconv.ParseUint(encodedGeneration, 10, 64)
		if parseErr != nil {
			return nil, newError(responses.ErrorMissingParameter, "invalid parameter bindingGeneration, err: %s", parseErr)
		}
		bindingGeneration = &generation
	}
	scope := playback.CommandScope{
		User: user.UserName, SessionID: p.StringOr("sessionId", ""), ClientID: p.StringOr("clientId", ""),
		BindingGeneration: bindingGeneration, OperationID: p.StringOr("operationId", ""),
	}
	status, err := api.playback.ExecuteCommand(ctx, scope, command)
	if errors.Is(err, playback.ErrSessionOwnership) {
		return nil, newError(responses.ErrorAuthorizationFail, "%s", err)
	}
	if errors.Is(err, playback.ErrSessionNotFound) {
		return nil, newError(responses.ErrorDataNotFound, "%s", err)
	}
	if errors.Is(err, playback.ErrCommandScope) {
		return nil, newError(responses.ErrorMissingParameter, "%s", err)
	}
	return createResponse(status, err)
}

// createResponse is to shorten the case-switch in the JukeboxController
func createResponse(status playback.DeviceStatus, err error) (*responses.Subsonic, error) {
	if err != nil {
		return nil, err
	}
	return statusResponse(status), nil
}

func statusResponse(status playback.DeviceStatus) *responses.Subsonic {
	response := newResponse()
	response.JukeboxStatus = deviceStatusToJukeboxStatus(status)
	return response
}

func deviceStatusToJukeboxStatus(status playback.DeviceStatus) *responses.JukeboxStatus {
	return &responses.JukeboxStatus{
		CurrentIndex: int32(status.CurrentIndex),
		Playing:      status.Playing,
		Gain:         status.Gain,
		Position:     int32(status.Position),
	}
}
