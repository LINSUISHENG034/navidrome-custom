package playback

import (
	"context"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
)

type sessionStatusStubServer struct {
	mediaFiles map[string]model.MediaFile
}

func (s *sessionStatusStubServer) Run(context.Context) error                        { return nil }
func (s *sessionStatusStubServer) GetDeviceForUser(string) (*playbackDevice, error) { return nil, nil }
func (s *sessionStatusStubServer) GetMediaFile(id string) (*model.MediaFile, error) {
	mf := s.mediaFiles[id]
	return &mf, nil
}
func (s *sessionStatusStubServer) ListDevices() []DeviceInfo                  { return nil }
func (s *sessionStatusStubServer) SwitchDevice(context.Context, string) error { return nil }
func (s *sessionStatusStubServer) RefreshDevices(context.Context) error       { return nil }
func (s *sessionStatusStubServer) AttachSession(context.Context, AttachRequest) (SessionStatus, error) {
	return SessionStatus{}, nil
}
func (s *sessionStatusStubServer) HeartbeatSession(context.Context, string, string) (SessionStatus, error) {
	return SessionStatus{}, nil
}
func (s *sessionStatusStubServer) DetachSession(context.Context, string, string) (SessionStatus, error) {
	return SessionStatus{}, nil
}
func (s *sessionStatusStubServer) SessionStatus(context.Context, string) (SessionStatus, error) {
	return SessionStatus{}, nil
}

func TestPlaybackServerDetachSessionReturnsSnapshot(t *testing.T) {
	ctx := context.Background()
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(time.Minute),
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
		},
	}
	ps.playbackDevices[0].PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	ps.playbackDevices[0].queueVersion = 7
	ps.playbackDevices[0].Gain = 0.8

	_, err := ps.AttachSession(ctx, AttachRequest{
		SessionID:  "detach-snapshot",
		User:       "admin",
		ClientID:   "tab-1",
		DeviceName: "pulse/test",
	})
	if err != nil {
		t.Fatalf("attach session: %v", err)
	}

	status, err := ps.DetachSession(ctx, "detach-snapshot", "tab-1")
	if err != nil {
		t.Fatalf("detach session: %v", err)
	}

	if status.Attached {
		t.Fatalf("expected detached snapshot, got %#v", status)
	}
	if status.DeviceName != "pulse/test" || status.TrackID != "1" || status.QueueVersion != 7 {
		t.Fatalf("unexpected detach snapshot: %#v", status)
	}
}

func TestPlaybackDeviceQueueVersionIncrementsOnSameLengthMutations(t *testing.T) {
	ctx := context.Background()
	stub := &sessionStatusStubServer{mediaFiles: map[string]model.MediaFile{
		"1": {ID: "1", Path: "/a.mp3"},
		"2": {ID: "2", Path: "/b.mp3"},
		"3": {ID: "3", Path: "/c.mp3"},
	}}
	pd := NewPlaybackDevice(ctx, stub, "Speaker", "auto")

	if _, err := pd.Add(ctx, []string{"1", "2"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	firstVersion := pd.queueVersion
	if firstVersion == 0 {
		t.Fatal("expected queueVersion to increment after add")
	}

	if _, err := pd.Remove(ctx, 0); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := pd.Add(ctx, []string{"3"}); err != nil {
		t.Fatalf("second add: %v", err)
	}

	if pd.PlaybackQueue.Size() != 2 {
		t.Fatalf("expected queue size 2, got %d", pd.PlaybackQueue.Size())
	}
	if pd.queueVersion <= firstVersion+1 {
		t.Fatalf("expected queueVersion to advance across same-length mutations, got %d after first version %d", pd.queueVersion, firstVersion)
	}
}
