package events

import "time"

type JukeboxStateUpdated struct {
	baseEvent
	SessionID     string    `json:"sessionId"`
	DeviceName    string    `json:"deviceName"`
	OwnerClientID string    `json:"ownerClientId"`
	CurrentIndex  int       `json:"currentIndex"`
	TrackID       string    `json:"trackId"`
	Playing       bool      `json:"playing"`
	Position      int       `json:"position"`
	Gain          float32   `json:"gain"`
	Attached      bool      `json:"attached"`
	QueueVersion  int       `json:"queueVersion"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}
