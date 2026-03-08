package playback

import (
	"context"

	"github.com/navidrome/navidrome/model/id"
	"github.com/navidrome/navidrome/model/request"
	serverevents "github.com/navidrome/navidrome/server/events"
)

func NewJukeboxStateUpdatedEvent(status SessionStatus) *serverevents.JukeboxStateUpdated {
	return &serverevents.JukeboxStateUpdated{
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
	}
}

func jukeboxTargetContext(username string) context.Context {
	ctx := request.WithUsername(context.Background(), username)
	return request.WithClientUniqueId(ctx, "jukebox-state-"+id.NewRandom())
}
