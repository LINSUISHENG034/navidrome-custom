package playback

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/navidrome/navidrome/core/playback/bluetooth"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	serverevents "github.com/navidrome/navidrome/server/events"
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

type capturedBrokerMessage struct {
	ctx   context.Context
	event any
}

type fakeEventBroker struct {
	sent       []capturedBrokerMessage
	broadcasts []capturedBrokerMessage
}

func (f *fakeEventBroker) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (f *fakeEventBroker) SendMessage(ctx context.Context, event serverevents.Event) {
	f.sent = append(f.sent, capturedBrokerMessage{ctx: ctx, event: event})
}

func (f *fakeEventBroker) SendBroadcastMessage(ctx context.Context, event serverevents.Event) {
	f.broadcasts = append(f.broadcasts, capturedBrokerMessage{ctx: ctx, event: event})
}

func TestNewJukeboxStateUpdatedEvent(t *testing.T) {
	staleSince := time.Unix(120, 0).UTC()
	status := SessionStatus{
		SessionID:         "s1",
		DeviceName:        "pulse/test",
		OwnerClientID:     "tab-1",
		CurrentIndex:      2,
		TrackID:           "track-3",
		Playing:           true,
		Position:          17,
		Gain:              0.8,
		Attached:          true,
		OwnershipState:    SessionOwnershipRecovering,
		TerminationReason: SessionTerminationStaleExpired,
		QueueVersion:      9,
		LastHeartbeat:     time.Unix(123, 0).UTC(),
		StaleSince:        &staleSince,
	}

	evt := NewJukeboxStateUpdatedEvent(status)
	if evt.SessionID != status.SessionID || evt.TrackID != status.TrackID || evt.QueueVersion != status.QueueVersion {
		t.Fatalf("unexpected event payload: %#v", evt)
	}
	if evt.OwnershipState != SessionOwnershipRecovering || evt.TerminationReason != SessionTerminationStaleExpired {
		t.Fatalf("expected durable-session metadata in event payload, got %#v", evt)
	}
	if evt.StaleSince == nil || !evt.StaleSince.Equal(staleSince) {
		t.Fatalf("expected staleSince in event payload, got %#v", evt)
	}
}

func TestPlaybackServerPublishJukeboxStateUpdatesTargetsMatchingDeviceSessions(t *testing.T) {
	ctx := context.Background()
	broker := &fakeEventBroker{}
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(time.Minute),
		eventBroker:    broker,
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
			*NewPlaybackDevice(ctx, nil, "Other", "pulse/other"),
		},
	}
	ps.playbackDevices[0].PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	ps.playbackDevices[0].queueVersion = 3
	ps.playbackDevices[0].Gain = 0.9
	ps.sessionManager.Attach(AttachRequest{SessionID: "s1", ClientID: "tab-1", User: "admin", DeviceName: "pulse/test"})
	ps.sessionManager.Attach(AttachRequest{SessionID: "s2", ClientID: "tab-2", User: "admin", DeviceName: "pulse/other"})

	ps.publishJukeboxStateUpdates(&ps.playbackDevices[0])

	if len(broker.sent) != 1 {
		t.Fatalf("expected 1 targeted event, got %d", len(broker.sent))
	}
	if len(broker.broadcasts) != 0 {
		t.Fatalf("expected no broadcast events, got %d", len(broker.broadcasts))
	}
	username, ok := request.UsernameFrom(broker.sent[0].ctx)
	if !ok || username != "admin" {
		t.Fatalf("expected targeted username context, got %q", username)
	}
	clientID, ok := request.ClientUniqueIdFrom(broker.sent[0].ctx)
	if !ok || clientID == "" {
		t.Fatal("expected synthetic clientUniqueId for same-user targeting")
	}
	state, ok := broker.sent[0].event.(*serverevents.JukeboxStateUpdated)
	if !ok {
		t.Fatalf("unexpected event type: %T", broker.sent[0].event)
	}
	if state.SessionID != "s1" || state.DeviceName != "pulse/test" || state.QueueVersion != 3 {
		t.Fatalf("unexpected event payload: %#v", state)
	}
}

func TestPlaybackServerPublishJukeboxStateUpdatesUsesMigratedSessionBinding(t *testing.T) {
	ctx := context.Background()
	broker := &fakeEventBroker{}
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(time.Minute),
		eventBroker:    broker,
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "alsa_output.analog"),
			*NewPlaybackDevice(ctx, nil, "BT", "pulse/bluez_output.AA_BB_CC.a2dp-sink"),
		},
	}
	ps.playbackDevices[0].Default = true
	ps.playbackDevices[0].PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	ps.playbackDevices[1].PlaybackQueue.Add(model.MediaFiles{{ID: "2", Path: "/b.mp3"}})

	_, err := ps.AttachSession(ctx, AttachRequest{
		SessionID:  "s1",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "alsa_output.analog",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := ps.SwitchDevice(ctx, "pulse/bluez_output.AA_BB_CC.a2dp-sink"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	broker.sent = nil

	ps.publishJukeboxStateUpdates(&ps.playbackDevices[1])

	if len(broker.sent) != 1 {
		t.Fatalf("expected 1 targeted event, got %d", len(broker.sent))
	}
	state, ok := broker.sent[0].event.(*serverevents.JukeboxStateUpdated)
	if !ok {
		t.Fatalf("unexpected event type: %T", broker.sent[0].event)
	}
	if state.SessionID != "s1" || state.DeviceName != "pulse/bluez_output.AA_BB_CC.a2dp-sink" {
		t.Fatalf("unexpected migrated event payload: %#v", state)
	}
}

func TestPlaybackServerReaperTransitionsHeartbeatLapseToRecovering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := &fakeEventBroker{}
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(50*time.Millisecond, 10*time.Minute),
		eventBroker:    broker,
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
		},
	}

	now := time.Now()
	ps.sessionManager.now = func() time.Time { return now }
	ps.sessionManager.Attach(AttachRequest{
		SessionID:  "s1",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "pulse/test",
	})

	now = now.Add(80 * time.Millisecond)
	ps.reapExpiredSessions()

	if len(broker.sent) != 1 {
		t.Fatalf("expected 1 recovering event, got %d", len(broker.sent))
	}
	state, ok := broker.sent[0].event.(*serverevents.JukeboxStateUpdated)
	if !ok {
		t.Fatalf("unexpected event type: %T", broker.sent[0].event)
	}
	if !state.Attached || state.OwnershipState != SessionOwnershipRecovering {
		t.Fatalf("expected attached recovering event, got %#v", state)
	}
	if state.StaleSince == nil {
		t.Fatalf("expected staleSince on recovering event, got %#v", state)
	}
}

func TestPlaybackServerSessionStatusReturnsRecoveringSession(t *testing.T) {
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
	ps.playbackDevices[0].PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})

	_, err := ps.AttachSession(ctx, AttachRequest{
		SessionID:  "s1",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "pulse/test",
	})
	if err != nil {
		t.Fatalf("attach session: %v", err)
	}

	now = now.Add(80 * time.Millisecond)
	ps.reapExpiredSessions()

	status, err := ps.SessionStatus(ctx, "s1")
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	if !status.Attached || status.OwnershipState != SessionOwnershipRecovering {
		t.Fatalf("expected recovering attached session status, got %#v", status)
	}
	if status.StaleSince == nil {
		t.Fatalf("expected staleSince in session status, got %#v", status)
	}
}

func TestPlaybackServerReaperBroadcastsDetachOnHardExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker := &fakeEventBroker{}
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(50*time.Millisecond, 100*time.Millisecond),
		eventBroker:    broker,
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
		},
	}

	now := time.Now()
	ps.sessionManager.now = func() time.Time { return now }
	ps.sessionManager.Attach(AttachRequest{
		SessionID:  "s1",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "pulse/test",
	})

	now = now.Add(80 * time.Millisecond)
	ps.reapExpiredSessions()
	now = now.Add(120 * time.Millisecond)
	ps.reapExpiredSessions()

	if len(broker.sent) != 2 {
		t.Fatalf("expected recovering + hard-expiry events, got %d", len(broker.sent))
	}
	state, ok := broker.sent[1].event.(*serverevents.JukeboxStateUpdated)
	if !ok {
		t.Fatalf("unexpected event type: %T", broker.sent[1].event)
	}
	if state.Attached || state.OwnershipState != SessionOwnershipDetached {
		t.Fatalf("expected detached terminal event, got %#v", state)
	}
	if state.TerminationReason != SessionTerminationStaleExpired {
		t.Fatalf("expected stale_expired termination, got %#v", state)
	}
}

func TestPlaybackServerCheckBluetoothConnectionsPublishesPauseState(t *testing.T) {
	ctx := context.Background()
	broker := &fakeEventBroker{}
	ps := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(time.Minute),
		eventBroker:    broker,
	}
	ps.playbackDevices = []playbackDevice{*ps.newPlaybackDevice(ctx, "BT", "pulse/bluez_output.AA_BB_CC.a2dp-sink")}
	ps.playbackDevices[0].Default = true
	ps.playbackDevices[0].PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	ps.playbackDevices[0].ActiveTrack = &mockTrack{playing: true, position: 12}

	_, err := ps.AttachSession(ctx, AttachRequest{
		SessionID:  "bt-session",
		ClientID:   "tab-1",
		User:       "admin",
		DeviceName: "pulse/bluez_output.AA_BB_CC.a2dp-sink",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	previousDiscover := discoverAllSinks
	discoverAllSinks = func(context.Context) []bluetooth.AudioSink { return nil }
	defer func() { discoverAllSinks = previousDiscover }()

	ps.checkBluetoothConnections(ctx)

	track, ok := ps.playbackDevices[0].ActiveTrack.(*mockTrack)
	if !ok {
		t.Fatalf("expected mockTrack, got %T", ps.playbackDevices[0].ActiveTrack)
	}
	if track.pauseCalls != 1 {
		t.Fatalf("expected paused track, pauseCalls=%d", track.pauseCalls)
	}
	if len(broker.sent) != 1 {
		t.Fatalf("expected 1 targeted event, got %d", len(broker.sent))
	}
	state, ok := broker.sent[0].event.(*serverevents.JukeboxStateUpdated)
	if !ok {
		t.Fatalf("unexpected event type: %T", broker.sent[0].event)
	}
	if state.Playing {
		t.Fatalf("expected published paused state, got %#v", state)
	}
}
