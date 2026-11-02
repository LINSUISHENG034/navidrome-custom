package events

import (
	"time"

	"github.com/navidrome/navidrome/model"
)

type JukeboxStateUpdated struct {
	baseEvent
	SessionID         string                           `json:"sessionId"`
	DeviceName        string                           `json:"deviceName"`
	OwnerClientID     string                           `json:"ownerClientId"`
	CurrentIndex      int                              `json:"currentIndex"`
	TrackID           string                           `json:"trackId"`
	QueueItemID       string                           `json:"queueItemId,omitempty"`
	Playing           bool                             `json:"playing"`
	Position          int                              `json:"position"`
	Gain              float32                          `json:"gain"`
	DesiredGain       float32                          `json:"desiredGain"`
	Muted             bool                             `json:"muted"`
	Attached          bool                             `json:"attached"`
	OwnershipState    string                           `json:"ownershipState"`
	TerminationReason string                           `json:"terminationReason,omitempty"`
	QueueVersion      int                              `json:"queueVersion"`
	StateSequence     uint64                           `json:"stateSequence"`
	PlayMode          string                           `json:"playMode"`
	QueueProtocol     *model.QueueProtocolCapabilities `json:"queueProtocol,omitempty"`
	LastHeartbeat     time.Time                        `json:"lastHeartbeat"`
	StaleSince        *time.Time                       `json:"staleSince,omitempty"`
	BindingGeneration uint64                           `json:"bindingGeneration"`
}
