package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
)

type durableScenarioHarness struct {
	ctx    context.Context
	now    time.Time
	server *playbackServer
	device *playbackDevice
}

func newDurableScenarioHarness(t *testing.T) *durableScenarioHarness {
	t.Helper()

	ctx := context.Background()
	server := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(50*time.Millisecond, 100*time.Millisecond),
		playbackDevices: []playbackDevice{
			*NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test"),
		},
	}
	harness := &durableScenarioHarness{
		ctx:    ctx,
		now:    time.Now(),
		server: server,
		device: &server.playbackDevices[0],
	}
	server.sessionManager.now = func() time.Time { return harness.now }
	return harness
}

func (h *durableScenarioHarness) advance(d time.Duration) {
	h.now = h.now.Add(d)
}

func (h *durableScenarioHarness) attach(t *testing.T, clientID string) SessionStatus {
	t.Helper()

	status, err := h.server.AttachSession(h.ctx, AttachRequest{
		SessionID:  "s1",
		ClientID:   clientID,
		User:       "admin",
		DeviceName: "pulse/test",
	})
	if err != nil {
		t.Fatalf("attach session: %v", err)
	}
	return status
}

func (h *durableScenarioHarness) configureQueue(trackIDs ...string) {
	files := make(model.MediaFiles, 0, len(trackIDs))
	for _, trackID := range trackIDs {
		files = append(files, model.MediaFile{ID: trackID, Path: "/" + trackID + ".mp3"})
	}
	h.device.PlaybackQueue.Add(files)
}

func (h *durableScenarioHarness) setCurrentIndex(index int) {
	h.device.PlaybackQueue.SetIndex(index)
}

func (h *durableScenarioHarness) startPlayback(t *testing.T) {
	t.Helper()
	h.device.ActiveTrack = &mockTrack{playing: true}

	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan bool, _ string, _ model.MediaFile) (Track, error) {
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	go h.device.trackSwitcherGoroutine()
}

func waitForSessionStatus(t *testing.T, server *playbackServer, sessionID string, predicate func(SessionStatus) bool) SessionStatus {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()

	for {
		select {
		case <-ticker.C:
			status, err := server.SessionStatus(context.Background(), sessionID)
			if err == nil && predicate(status) {
				return status
			}
		case <-deadline.C:
			t.Fatalf("session status for %q did not satisfy predicate in time", sessionID)
		}
	}
}

func TestDurableSessionDoesNotRewindAfterRemoteNaturalAdvance(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.startPlayback(t)
	h.attach(t, "tab-1")

	h.advance(80 * time.Millisecond)
	h.server.reapExpiredSessions()
	h.device.PlaybackDone <- true

	status := waitForSessionStatus(t, h.server, "s1", func(status SessionStatus) bool {
		return status.CurrentIndex == 1 && status.TrackID == "2"
	})
	if !status.Attached || status.OwnershipState != SessionOwnershipRecovering {
		t.Fatalf("expected recovering durable authority after remote advance, got %#v", status)
	}
}

func TestDurableSessionSurvivesHeartbeatLapseWhilePlaybackContinues(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.startPlayback(t)
	h.attach(t, "tab-1")

	h.advance(80 * time.Millisecond)
	h.server.reapExpiredSessions()

	status, err := h.server.SessionStatus(h.ctx, "s1")
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	if !status.Attached || status.OwnershipState != SessionOwnershipRecovering {
		t.Fatalf("expected recovering attached session, got %#v", status)
	}
	if status.CurrentIndex != 0 || status.TrackID != "1" {
		t.Fatalf("expected playback state to remain authoritative, got %#v", status)
	}
}

func TestDurableSessionHiddenTabReconnectPreservesRemoteAdvance(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.startPlayback(t)
	h.attach(t, "tab-1")

	h.advance(80 * time.Millisecond)
	h.server.reapExpiredSessions()
	h.device.PlaybackDone <- true

	advanced := waitForSessionStatus(t, h.server, "s1", func(status SessionStatus) bool {
		return status.OwnershipState == SessionOwnershipRecovering &&
			status.CurrentIndex == 1 &&
			status.TrackID == "2"
	})
	if advanced.StaleSince == nil {
		t.Fatalf("expected staleSince to mark reconnect gap, got %#v", advanced)
	}

	h.advance(20 * time.Millisecond)
	recovered, err := h.server.HeartbeatSession(h.ctx, "s1", "tab-1")
	if err != nil {
		t.Fatalf("heartbeat recovery: %v", err)
	}
	if recovered.OwnershipState != SessionOwnershipAttached || recovered.TrackID != "2" {
		t.Fatalf("expected recovered attached session with remote advance preserved, got %#v", recovered)
	}
}

func TestDurableSessionTakeoverRejectsOldOwnerWithoutRewindingRemoteState(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2", "3")
	h.setCurrentIndex(2)
	h.attach(t, "tab-1")
	takenOver := h.attach(t, "tab-2")

	if takenOver.OwnerClientID != "tab-2" || takenOver.CurrentIndex != 2 || takenOver.TrackID != "3" {
		t.Fatalf("expected takeover to preserve remote state, got %#v", takenOver)
	}

	if _, err := h.server.HeartbeatSession(h.ctx, "s1", "tab-1"); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected old owner heartbeat to fail with ownership error, got %v", err)
	}

	status, err := h.server.SessionStatus(h.ctx, "s1")
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	if status.OwnerClientID != "tab-2" || status.CurrentIndex != 2 {
		t.Fatalf("expected new owner to remain authoritative, got %#v", status)
	}
}

func TestDurableSessionPageReloadRehydratesRemoteStateForNewOwner(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.setCurrentIndex(1)
	h.attach(t, "tab-1")

	status := h.attach(t, "tab-2")
	if status.OwnerClientID != "tab-2" || status.CurrentIndex != 1 || status.TrackID != "2" {
		t.Fatalf("expected reload attach to rehydrate remote state, got %#v", status)
	}
}

func TestDurableSessionCanReattachAfterTier2Expiry(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.setCurrentIndex(1)
	h.attach(t, "tab-1")

	h.advance(80 * time.Millisecond)
	h.server.reapExpiredSessions()
	h.advance(120 * time.Millisecond)
	h.server.reapExpiredSessions()

	if _, err := h.server.SessionStatus(h.ctx, "s1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected session not found after tier 2 expiry, got %v", err)
	}

	status := h.attach(t, "tab-1")
	if status.OwnershipState != SessionOwnershipAttached || status.CurrentIndex != 1 || status.TrackID != "2" {
		t.Fatalf("expected re-attach to pick up current remote state, got %#v", status)
	}
}

func TestDurableSessionSuspendResumeReattachesToCurrentRemoteSnapshot(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2", "3")
	h.setCurrentIndex(2)
	h.attach(t, "tab-1")

	h.advance(80 * time.Millisecond)
	h.server.reapExpiredSessions()
	h.advance(120 * time.Millisecond)
	h.server.reapExpiredSessions()

	status := h.attach(t, "tab-1")
	if status.OwnershipState != SessionOwnershipAttached || status.CurrentIndex != 2 || status.TrackID != "3" {
		t.Fatalf("expected suspend/resume re-attach to recover current remote snapshot, got %#v", status)
	}
}
