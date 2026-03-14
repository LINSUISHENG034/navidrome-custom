package playback

import (
	"context"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
)

func TestDurableSessionDoesNotRewindAfterRemoteNaturalAdvance(t *testing.T) {
	ctx := context.Background()
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(50*time.Millisecond, 10*time.Minute),
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
		},
	}

	now := time.Now()
	ps.sessionManager.now = func() time.Time { return now }

	device := &ps.playbackDevices[0]
	device.PlaybackQueue.Add(model.MediaFiles{
		{ID: "1", Path: "/a.mp3"},
		{ID: "2", Path: "/b.mp3"},
	})
	device.ActiveTrack = &mockTrack{playing: true}

	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan bool, _ string, _ model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	defer func() { newTrack = previousNewTrack }()

	_, err := ps.AttachSession(ctx, AttachRequest{
		SessionID:  "s1",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "pulse/test",
	})
	if err != nil {
		t.Fatalf("attach session: %v", err)
	}

	go device.trackSwitcherGoroutine()

	now = now.Add(80 * time.Millisecond)
	ps.reapExpiredSessions()
	device.PlaybackDone <- true

	deadline := time.Now().Add(time.Second)
	for {
		status, err := ps.SessionStatus(ctx, "s1")
		if err == nil && status.CurrentIndex == 1 && status.TrackID == "2" {
			if !status.Attached || status.OwnershipState != SessionOwnershipRecovering {
				t.Fatalf("expected recovering durable authority after remote advance, got %#v", status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session status did not converge to remote natural advance in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
