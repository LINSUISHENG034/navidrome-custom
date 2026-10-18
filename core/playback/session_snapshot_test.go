package playback

import (
	"context"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
)

type snapshotTrack struct {
	playing  bool
	position int
}

func (t *snapshotTrack) IsPlaying() bool           { return t.playing }
func (t *snapshotTrack) SetVolume(float32)         {}
func (t *snapshotTrack) Pause()                    { t.playing = false }
func (t *snapshotTrack) Unpause()                  { t.playing = true }
func (t *snapshotTrack) Position() int             { return t.position }
func (t *snapshotTrack) SetPosition(pos int) error { t.position = pos; return nil }
func (t *snapshotTrack) Close()                    {}
func (t *snapshotTrack) String() string            { return "snapshotTrack" }

func TestPlaybackServerSessionSnapshotReturnsAuthorizedBoundQueue(t *testing.T) {
	ctx := context.Background()
	defaultDevice := NewPlaybackDevice(ctx, nil, "Default", "pulse/default")
	defaultDevice.Default = true
	defaultDevice.PlaybackQueue.Set(model.MediaFiles{{ID: "default-only", Title: "Not in session queue"}})
	device := NewPlaybackDevice(ctx, nil, "Bluetooth", "pulse/bluez_test")
	device.PlaybackQueue.Set(model.MediaFiles{
		{ID: "first", Title: "First", Artist: "Artist A", Album: "Album A", Duration: 10},
		{ID: "repeat", Title: "Earlier duplicate", Artist: "Artist B", Album: "Album B", Duration: 20},
		{ID: "repeat", Title: "Current duplicate", Artist: "Artist C", Album: "Album C", Duration: 30},
	})
	device.PlaybackQueue.SetIndex(2)
	device.ActiveTrack = &snapshotTrack{playing: true, position: 23}
	device.Gain = 0.42
	device.queueVersion = 17

	ps := &playbackServer{
		sessionManager: NewSessionManager(time.Minute),
		playbackDevices: []*playbackDevice{
			defaultDevice,
			device,
		},
	}
	ps.sessionManager.Attach(AttachRequest{
		SessionID:  "session-a",
		User:       "alice",
		ClientID:   "tab-a",
		DeviceName: "pulse/bluez_test",
	})

	snapshot, err := ps.SessionSnapshot(ctx, "session-a", "alice")
	if err != nil {
		t.Fatalf("get session snapshot: %v", err)
	}
	if snapshot.SessionStatus.DeviceName != "pulse/bluez_test" || snapshot.SessionStatus.CurrentIndex != 2 || snapshot.SessionStatus.TrackID != "repeat" {
		t.Fatalf("unexpected session status: %#v", snapshot.SessionStatus)
	}
	if snapshot.SessionStatus.QueueVersion != 17 || !snapshot.SessionStatus.Playing || snapshot.SessionStatus.Position != 23 || snapshot.SessionStatus.Gain != 0.42 {
		t.Fatalf("unexpected session playback state: %#v", snapshot.SessionStatus)
	}
	playlist := snapshot.JukeboxPlaylist
	if playlist.CurrentIndex != snapshot.SessionStatus.CurrentIndex || playlist.Playing != snapshot.SessionStatus.Playing || playlist.Position != snapshot.SessionStatus.Position || playlist.Gain != snapshot.SessionStatus.Gain {
		t.Fatalf("playlist state does not match session status: %#v != %#v", playlist, snapshot.SessionStatus)
	}
	if len(playlist.Entry) != 3 || playlist.Entry[playlist.CurrentIndex].ID != snapshot.SessionStatus.TrackID || playlist.Entry[playlist.CurrentIndex].Title != "Current duplicate" {
		t.Fatalf("snapshot lost queue order or duplicate index: %#v", playlist.Entry)
	}
	if got := playlist.Entry[playlist.CurrentIndex]; got.Artist != "Artist C" || got.Album != "Album C" || got.Duration != 30 {
		t.Fatalf("unexpected current playlist entry: %#v", got)
	}

	device.mu.Lock()
	device.PlaybackQueue.Items[2].Title = "mutated after snapshot"
	device.mu.Unlock()
	if snapshot.JukeboxPlaylist.Entry[2].Title != "Current duplicate" {
		t.Fatalf("snapshot retained mutable queue data: %#v", snapshot.JukeboxPlaylist.Entry[2])
	}

	if _, err := ps.SessionSnapshot(ctx, "session-a", "bob"); err != ErrSessionOwnership {
		t.Fatalf("expected other user's session to be forbidden, got %v", err)
	}
}

func TestPlaybackServerSessionSnapshotFollowsMigratedDeviceBinding(t *testing.T) {
	ctx := context.Background()
	oldDevice := NewPlaybackDevice(ctx, nil, "Default", "pulse/default")
	oldDevice.Default = true
	oldDevice.PlaybackQueue.Set(model.MediaFiles{
		{ID: "before", Title: "Before"},
		{ID: "current", Title: "Current after migration", Artist: "Artist", Album: "Album", Duration: 42},
	})
	oldDevice.PlaybackQueue.SetIndex(1)
	oldDevice.Gain = 0.65
	newDevice := NewPlaybackDevice(ctx, nil, "Bluetooth", "pulse/bluez_test")
	newDevice.PlaybackQueue.Set(model.MediaFiles{{ID: "stale", Title: "Stale"}})

	ps := &playbackServer{
		sessionManager: NewSessionManager(time.Minute),
		playbackDevices: []*playbackDevice{
			oldDevice,
			newDevice,
		},
	}
	ps.sessionManager.Attach(AttachRequest{
		SessionID:  "migrated-session",
		User:       "alice",
		ClientID:   "tab-a",
		DeviceName: "pulse/default",
	})

	if err := ps.SwitchDevice(ctx, "pulse/bluez_test"); err != nil {
		t.Fatalf("switch device: %v", err)
	}
	snapshot, err := ps.SessionSnapshot(ctx, "migrated-session", "alice")
	if err != nil {
		t.Fatalf("get migrated snapshot: %v", err)
	}
	if snapshot.SessionStatus.DeviceName != "pulse/bluez_test" || snapshot.SessionStatus.TrackID != "current" {
		t.Fatalf("snapshot did not follow migrated session binding: %#v", snapshot.SessionStatus)
	}
	if snapshot.JukeboxPlaylist.CurrentIndex != 1 || len(snapshot.JukeboxPlaylist.Entry) != 2 || snapshot.JukeboxPlaylist.Entry[1].Title != "Current after migration" || snapshot.JukeboxPlaylist.Gain != 0.65 {
		t.Fatalf("snapshot did not follow migrated playback state: %#v", snapshot.JukeboxPlaylist)
	}
}
