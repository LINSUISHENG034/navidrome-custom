package playback

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
)

type durableScenarioHarness struct {
	ctx    context.Context
	now    time.Time
	server *playbackServer
	device *playbackDevice
	token  model.PlaybackToken
}

func newDurableScenarioHarness(t *testing.T) *durableScenarioHarness {
	t.Helper()

	ctx := context.Background()
	dev := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/test")
	dev.Default = true
	server := &playbackServer{
		ctx:            &ctx,
		sessionManager: NewSessionManager(50*time.Millisecond, 100*time.Millisecond),
		playbackDevices: []*playbackDevice{
			dev,
		},
	}
	harness := &durableScenarioHarness{
		ctx:    ctx,
		now:    time.Now(),
		server: server,
		device: server.playbackDevices[0],
	}
	server.sessionManager.now = func() time.Time { return harness.now }
	server.now = func() time.Time { return harness.now }
	return harness
}

func (h *durableScenarioHarness) advance(d time.Duration) {
	h.now = h.now.Add(d)
}

func (h *durableScenarioHarness) attach(t *testing.T, clientID string, takeover ...bool) SessionStatus {
	t.Helper()

	status, err := h.server.AttachSession(h.ctx, AttachRequest{
		SessionID:  "s1",
		ClientID:   clientID,
		User:       "admin",
		DeviceName: "pulse/test",
		Takeover:   len(takeover) > 0 && takeover[0],
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

	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		h.token = token
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	if _, err := h.device.Start(h.ctx); err != nil {
		t.Fatalf("start playback: %v", err)
	}
}

func (h *durableScenarioHarness) sendNaturalEnd() {
	h.device.PlaybackEvents <- model.PlaybackEvent{
		Token: h.token,
		Kind:  model.PlaybackEventNaturalEnd,
	}
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
	h.sendNaturalEnd()

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
	h.sendNaturalEnd()

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

func TestDurableSessionOrdinaryAttachDoesNotStealActiveOwner(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.setCurrentIndex(1)
	h.startPlayback(t)
	if _, err := h.device.Skip(h.ctx, 1, 17); err != nil {
		t.Fatal(err)
	}
	h.attach(t, "tab-1")
	_, err := h.server.AttachSession(h.ctx, AttachRequest{
		SessionID: "s1", ClientID: "tab-2", User: "admin", DeviceName: "pulse/test",
	})
	if !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("ordinary new-page attach stole ownership: %v", err)
	}
	state, err := h.server.SessionStatus(h.ctx, "s1")
	if err != nil || state.OwnerClientID != "tab-1" || state.CurrentIndex != 1 || state.TrackID != "2" || state.Position != 17 || !state.Playing {
		t.Fatalf("denied attach changed playback authority: %#v err=%v", state, err)
	}
}

func TestDurableSessionTakeoverRejectsOldOwnerWithoutRewindingRemoteState(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2", "3")
	h.setCurrentIndex(2)
	h.attach(t, "tab-1")
	takenOver := h.attach(t, "tab-2", true)

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

	status := h.attach(t, "tab-2", true)
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

func TestDurableSessionGenerationMatrix(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	status := h.attach(t, "owner-tab")

	if status.BindingGeneration != 1 {
		t.Fatalf("expected initial generation 1, got %d", status.BindingGeneration)
	}

	ctx := h.ctx
	sessionID := "s1"
	clientID := "owner-tab"

	// 1. Missing generation -> ErrGenerationRequired
	scopeMissing := CommandScope{User: "admin", SessionID: sessionID, ClientID: clientID}
	if _, err := h.server.ExecuteCommand(ctx, scopeMissing, PlaybackCommand{Action: CommandSet, IDs: []string{"1"}}); !errors.Is(err, ErrGenerationRequired) {
		t.Fatalf("expected ErrGenerationRequired for nil generation, got %v", err)
	}

	// 2. Zero generation -> ErrGenerationRequired
	zero := uint64(0)
	scopeZero := CommandScope{User: "admin", SessionID: sessionID, ClientID: clientID, BindingGeneration: &zero}
	if _, err := h.server.ExecuteCommand(ctx, scopeZero, PlaybackCommand{Action: CommandSet, IDs: []string{"1"}}); !errors.Is(err, ErrGenerationRequired) {
		t.Fatalf("expected ErrGenerationRequired for 0 generation, got %v", err)
	}

	// 3. Future generation -> ErrCommandSuperseded with zero target side effects
	future := uint64(5)
	scopeFuture := CommandScope{User: "admin", SessionID: sessionID, ClientID: clientID, BindingGeneration: &future}
	if _, err := h.server.ExecuteCommand(ctx, scopeFuture, PlaybackCommand{Action: CommandGain, Gain: 0.125}); !errors.Is(err, ErrCommandSuperseded) {
		t.Fatalf("expected ErrCommandSuperseded for future generation, got %v", err)
	}
	if h.device.Gain == 0.125 {
		t.Fatal("future generation modified target device side effects!")
	}

	// 4. Exact generation (1) -> succeeds
	gen1 := uint64(1)
	scope1 := CommandScope{User: "admin", SessionID: sessionID, ClientID: clientID, BindingGeneration: &gen1, OperationID: "g1:generation-exact"}
	if st, err := h.server.ExecuteCommand(ctx, scope1, PlaybackCommand{Action: CommandGain, Gain: 0.5}); err != nil || st.Gain != 0.5 {
		t.Fatalf("exact generation failed: %v", err)
	}

	// 5. Same-owner reattach preserves generation 1
	reattachStatus := h.attach(t, "owner-tab")
	if reattachStatus.BindingGeneration != 1 {
		t.Fatalf("same-owner reattach changed generation: %d", reattachStatus.BindingGeneration)
	}

	// 6. Explicit takeover increments generation to 2
	takeoverStatus := h.attach(t, "new-tab", true)
	if takeoverStatus.BindingGeneration != 2 {
		t.Fatalf("takeover did not increment generation: %d", takeoverStatus.BindingGeneration)
	}

	// 7. Old owner with old generation -> ErrSessionOwnership
	if _, err := h.server.ExecuteCommand(ctx, scope1, PlaybackCommand{Action: CommandGain, Gain: 0.75}); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected ErrSessionOwnership for old owner, got %v", err)
	}

	// 8. New owner with older generation 1 -> ErrCommandSuperseded
	scopeNewOldGen := CommandScope{User: "admin", SessionID: sessionID, ClientID: "new-tab", BindingGeneration: &gen1}
	if _, err := h.server.ExecuteCommand(ctx, scopeNewOldGen, PlaybackCommand{Action: CommandGain, Gain: 0.75}); !errors.Is(err, ErrCommandSuperseded) {
		t.Fatalf("expected ErrCommandSuperseded for older generation on new owner, got %v", err)
	}
	if h.device.Gain == 0.75 {
		t.Fatal("older generation modified target device side effects!")
	}

	// 9. New owner with exact generation 2 -> succeeds
	gen2 := uint64(2)
	scopeNewExact := CommandScope{User: "admin", SessionID: sessionID, ClientID: "new-tab", BindingGeneration: &gen2, OperationID: "g2:generation-exact"}
	if st, err := h.server.ExecuteCommand(ctx, scopeNewExact, PlaybackCommand{Action: CommandGain, Gain: 0.75}); err != nil || st.Gain != 0.75 {
		t.Fatalf("exact generation 2 failed on new owner: %v", err)
	}

	// 10. Legacy access to protected context is rejected
	legacyScope := CommandScope{User: "admin"}
	if _, err := h.server.ExecuteCommand(ctx, legacyScope, PlaybackCommand{Action: CommandGain, Gain: 0.9}); !errors.Is(err, ErrProtectedContextConflict) {
		t.Fatalf("expected ErrProtectedContextConflict for legacy access to protected context, got %v", err)
	}
}

func TestDurableSessionOperationStoreDeduplicationAndEviction(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.attach(t, "tab-1")
	gen1 := uint64(1)

	ctx := h.ctx
	scope := CommandScope{
		User:              "admin",
		SessionID:         "s1",
		ClientID:          "tab-1",
		BindingGeneration: &gen1,
		OperationID:       "g1:unique-1",
	}

	// First execution applies
	st1, err := h.server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandGain, Gain: 0.4})
	if err != nil || st1.Gain != 0.4 {
		t.Fatalf("first execution failed: %v", err)
	}

	// Sequential retry with same OperationID returns identical result without re-executing
	st2, err := h.server.ExecuteCommand(ctx, scope, PlaybackCommand{Action: CommandGain, Gain: 0.4})
	if err != nil || st2.Gain != 0.4 {
		t.Fatalf("sequential retry failed: %v", err)
	}

	// Query operation result returns APPLIED
	rec, err := h.server.QueryOperation(ctx, "s1", "g1:unique-1")
	if err != nil || rec.Status != OperationStatusApplied {
		t.Fatalf("queryOperation failed: %#v, %v", rec, err)
	}

	// Concurrent execution across 10 goroutines with same OperationID
	concurrentScope := CommandScope{
		User:              "admin",
		SessionID:         "s1",
		ClientID:          "tab-1",
		BindingGeneration: &gen1,
		OperationID:       "g1:concurrent-1",
	}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	results := make([]DeviceStatus, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = h.server.ExecuteCommand(ctx, concurrentScope, PlaybackCommand{Action: CommandGain, Gain: 0.6})
		}(i)
	}
	wg.Wait()
	for i := 0; i < 10; i++ {
		if errs[i] != nil || results[i].Gain != 0.6 {
			t.Fatalf("concurrent execution goroutine %d failed: %v, %#v", i, errs[i], results[i])
		}
	}

	// Bounded capacity eviction
	store := NewOperationStore(3, time.Hour)
	store.Complete("s1", "op-1", OperationRecord{Status: OperationStatusApplied})
	store.Complete("s1", "op-2", OperationRecord{Status: OperationStatusApplied})
	store.Complete("s1", "op-3", OperationRecord{Status: OperationStatusApplied})
	store.Complete("s1", "op-4", OperationRecord{Status: OperationStatusApplied}) // evicts op-1

	if rec, found := store.Get("s1", "op-1"); found || rec.Status != OperationStatusUnknown {
		t.Fatalf("expected evicted op-1 to return UNKNOWN, got found=%v, status=%s", found, rec.Status)
	}
	if rec, found := store.Get("s1", "op-4"); !found || rec.Status != OperationStatusApplied {
		t.Fatalf("expected op-4 to remain in store, got found=%v, %#v", found, rec)
	}

	// TTL eviction
	now := time.Now()
	storeWithClock := NewOperationStore(10, 50*time.Millisecond, func() time.Time { return now })
	storeWithClock.Complete("s1", "op-ttl", OperationRecord{Status: OperationStatusApplied})
	now = now.Add(60 * time.Millisecond)
	if rec, found := storeWithClock.Get("s1", "op-ttl"); found || rec.Status != OperationStatusUnknown {
		t.Fatalf("expected expired op-ttl to return UNKNOWN, got found=%v, status=%s", found, rec.Status)
	}
}

func TestDurableSessionTargetOccupancyAndNonDestructiveReservation(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	h.startPlayback(t)
	h.attach(t, "target-owner")

	ctx := h.ctx
	targetDevice := "pulse/test"
	sourceDevice := NewPlaybackDevice(ctx, h.server, "Source", "source-device")
	h.server.playbackDevices = append(h.server.playbackDevices, sourceDevice)
	sourceStatus, err := h.server.AttachSession(ctx, AttachRequest{SessionID: "source-session", User: "admin", ClientID: "source-client", DeviceName: "source-device"})
	if err != nil {
		t.Fatal(err)
	}
	otherDevice := NewPlaybackDevice(ctx, h.server, "Other Source", "other-source-device")
	h.server.playbackDevices = append(h.server.playbackDevices, otherDevice)
	otherStatus, err := h.server.AttachSession(ctx, AttachRequest{SessionID: "other-session", User: "admin", ClientID: "other-client", DeviceName: "other-source-device"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Admission to occupied target without confirmation returns 409 TargetOccupiedError
	resReq := ReserveMigrationRequest{
		MigrationID:       "mig-1",
		OperationID:       "g1:reserve-occupied",
		SessionID:         "source-session",
		ClientID:          "source-client",
		User:              "admin",
		DeviceName:        targetDevice,
		BindingGeneration: &sourceStatus.BindingGeneration,
	}
	_, err = h.server.ReserveMigration(ctx, resReq)
	var occupiedErr *TargetOccupiedError
	if !errors.As(err, &occupiedErr) {
		t.Fatalf("expected TargetOccupiedError, got: %v", err)
	}
	if occupiedErr.SceneFingerprint == "" || occupiedErr.QueueCount != 2 {
		t.Fatalf("unexpected occupied error details: %#v", occupiedErr)
	}
	firstFp := occupiedErr.SceneFingerprint

	// Target remains completely undisturbed
	targetStatus, _ := h.server.SessionStatus(ctx, "s1")
	if targetStatus.OwnerClientID != "target-owner" || !targetStatus.Playing || targetStatus.QueueVersion != h.device.queueVersion {
		t.Fatalf("target was disturbed before confirmation: %#v", targetStatus)
	}

	// A fingerprint alone is not confirmation of an occupied target.
	resReq.ExpectedFingerprint = firstFp
	resReq.OperationID = "g1:reserve-fingerprint-only"
	if _, err := h.server.ReserveMigration(ctx, resReq); !errors.As(err, &occupiedErr) {
		t.Fatalf("fingerprint without explicit takeover must remain occupied: %v", err)
	}
	resReq.ForceTakeover = true
	resReq.OperationID = "g1:reserve-confirmed"
	res, err := h.server.ReserveMigration(ctx, resReq)
	if err != nil || res.Status != OperationStatusApplied {
		t.Fatalf("reservation with matching fingerprint failed: %v, %#v", err, res)
	}

	// Target is STILL untouched
	targetStatus2, _ := h.server.SessionStatus(ctx, "s1")
	if targetStatus2.OwnerClientID != "target-owner" || !targetStatus2.Playing {
		t.Fatalf("reservation evicted target owner or stopped playback: %#v", targetStatus2)
	}

	// 3. Conflicting reservation from another migration is rejected
	conflictingReq := ReserveMigrationRequest{
		MigrationID:         "mig-2",
		OperationID:         "g1:reserve-conflict",
		SessionID:           "other-session",
		ClientID:            "other-client",
		User:                "admin",
		DeviceName:          targetDevice,
		ExpectedFingerprint: firstFp,
		BindingGeneration:   &otherStatus.BindingGeneration,
	}
	_, err = h.server.ReserveMigration(ctx, conflictingReq)
	if !errors.Is(err, ErrMigrationConflict) {
		t.Fatalf("expected ErrMigrationConflict on conflicting reservation, got: %v", err)
	}

	// 4. Query reservation returns ACTIVE
	qRes, err := h.server.QueryReservation(ctx, "mig-1")
	if err != nil || qRes.Status != string(ReservationActive) {
		t.Fatalf("expected ACTIVE reservation, got %#v, %v", qRes, err)
	}

	// 5. Release reservation is idempotent
	releaseReq := ReleaseMigrationRequest{MigrationID: "mig-1", OperationID: "g1:release", SessionID: "source-session", ClientID: "source-client", User: "admin", BindingGeneration: &sourceStatus.BindingGeneration}
	relRes, err := h.server.ReleaseMigrationReservation(ctx, releaseReq)
	if err != nil || relRes.Status != OperationStatusApplied {
		t.Fatalf("release failed: %v, %#v", err, relRes)
	}
	// Repeated release returns APPLIED (idempotent)
	relRes2, err := h.server.ReleaseMigrationReservation(ctx, releaseReq)
	if err != nil || relRes2.Status != OperationStatusApplied {
		t.Fatalf("repeated release failed: %v, %#v", err, relRes2)
	}

	// Query reservation now returns RELEASED
	qResReleased, err := h.server.QueryReservation(ctx, "mig-1")
	if err != nil || qResReleased.Status != string(ReservationReleased) {
		t.Fatalf("expected RELEASED reservation, got %#v, %v", qResReleased, err)
	}
}

func TestDurableSessionCommitMigrationAtomicity(t *testing.T) {
	h := newDurableScenarioHarness(t)
	ctx := h.ctx

	// Add target device
	targetDev := NewPlaybackDevice(ctx, h.server, "Target Speaker", "target-dev")
	targetDev.PlaybackQueue.Add(model.MediaFiles{{ID: "target-1", Path: "/t1.mp3"}})
	h.server.playbackDevices = append(h.server.playbackDevices, targetDev)

	// Create source session on pulse/test
	sourceSession, err := h.server.AttachSession(ctx, AttachRequest{
		SessionID:  "source-session",
		User:       "admin",
		ClientID:   "source-tab",
		DeviceName: "pulse/test",
	})
	if err != nil {
		t.Fatalf("attach source: %v", err)
	}

	// Reserve target
	fp := targetDev.SceneFingerprint()
	resReq := ReserveMigrationRequest{
		MigrationID:         "mig-commit-1",
		OperationID:         "g1:reserve-commit",
		SessionID:           "source-session",
		ClientID:            "source-tab",
		User:                "admin",
		DeviceName:          "target-dev",
		ForceTakeover:       true,
		ExpectedFingerprint: fp,
		BindingGeneration:   &sourceSession.BindingGeneration,
	}
	if _, err := h.server.ReserveMigration(ctx, resReq); err != nil {
		t.Fatalf("reserve failed: %v", err)
	}

	// Third-party modifies target scene between reserve and commit
	targetDev.mu.Lock()
	targetDev.incrementSceneRevisionLocked()
	targetDev.mu.Unlock()

	// Commit with stale fingerprint is rejected, preserving third-party scene
	commitReq := CommitMigrationRequest{
		MigrationID:         "mig-commit-1",
		OperationID:         "g1:commit-stale",
		SessionID:           "source-session",
		ClientID:            "source-tab",
		User:                "admin",
		TargetDeviceName:    "target-dev",
		ExpectedGeneration:  sourceSession.BindingGeneration,
		ExpectedFingerprint: fp,
	}
	_, err = h.server.CommitMigration(ctx, commitReq)
	var occupiedErr *TargetOccupiedError
	if !errors.As(err, &occupiedErr) {
		t.Fatalf("expected TargetOccupiedError when scene changed before commit, got: %v", err)
	}

	// Update reservation with new fingerprint
	newFp := targetDev.SceneFingerprint()
	resReq.ExpectedFingerprint = newFp
	resReq.OperationID = "g1:reserve-commit-renewed"
	if _, err := h.server.ReserveMigration(ctx, resReq); err != nil {
		t.Fatalf("re-reserve failed: %v", err)
	}

	// Commit succeeds atomically
	commitReq.ExpectedFingerprint = newFp
	commitReq.OperationID = "g1:commit-final"
	position := 42
	gain := float32(0.8)
	commitReq.Position = &position
	commitReq.Gain = &gain
	committedStatus, err := h.server.CommitMigration(ctx, commitReq)
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	if committedStatus.BindingGeneration != sourceSession.BindingGeneration+1 {
		t.Fatalf("expected generation increment on commit: %d vs %d", committedStatus.BindingGeneration, sourceSession.BindingGeneration+1)
	}
	if committedStatus.DeviceName != "target-dev" {
		t.Fatalf("expected device target-dev, got %s", committedStatus.DeviceName)
	}
}

func TestDurableSessionRefreshHandoffContract(t *testing.T) {
	h := newDurableScenarioHarness(t)
	h.configureQueue("1", "2")
	status := h.attach(t, "tab-1")

	if status.ControlHandoffToken == "" {
		t.Fatal("expected attach response to issue handoff token to owner")
	}
	initialToken := status.ControlHandoffToken

	// Observer/status query does NOT disclose the handoff token
	statusQuery, err := h.server.SessionStatus(h.ctx, "s1")
	if err != nil || statusQuery.ControlHandoffToken != "" {
		t.Fatalf("status query disclosed handoff token: %#v", statusQuery)
	}

	// Snapshot query does NOT disclose handoff token
	snapQuery, err := h.server.SessionSnapshot(h.ctx, "s1", "admin")
	if err != nil || snapQuery.SessionStatus.ControlHandoffToken != "" {
		t.Fatalf("snapshot query disclosed handoff token: %#v", snapQuery)
	}

	// Handoff to new document tab-2
	handoffStatus, err := h.server.HandoffSession(h.ctx, HandoffRequest{
		SessionID:    "s1",
		User:         "admin",
		NewClientID:  "tab-2",
		HandoffToken: initialToken,
	})
	if err != nil {
		t.Fatalf("handoff failed: %v", err)
	}
	if handoffStatus.OwnerClientID != "tab-2" {
		t.Fatalf("expected owner tab-2, got %s", handoffStatus.OwnerClientID)
	}
	if handoffStatus.BindingGeneration != 2 {
		t.Fatalf("expected generation 2 after handoff, got %d", handoffStatus.BindingGeneration)
	}
	if handoffStatus.ControlHandoffToken == "" || handoffStatus.ControlHandoffToken == initialToken {
		t.Fatalf("expected rotated handoff token on handoff response: %s", handoffStatus.ControlHandoffToken)
	}

	// Clone competition: tab-3 reusing initialToken is rejected
	_, err = h.server.HandoffSession(h.ctx, HandoffRequest{
		SessionID:    "s1",
		User:         "admin",
		NewClientID:  "tab-3",
		HandoffToken: initialToken,
	})
	if !errors.Is(err, ErrHandoffInvalid) {
		t.Fatalf("expected ErrHandoffInvalid on reused token, got: %v", err)
	}

	// Old document tab-1 cannot continue writing
	gen1 := uint64(1)
	oldDocScope := CommandScope{
		User:              "admin",
		SessionID:         "s1",
		ClientID:          "tab-1",
		BindingGeneration: &gen1,
	}
	if _, err := h.server.ExecuteCommand(h.ctx, oldDocScope, PlaybackCommand{Action: CommandGain, Gain: 0.1}); !errors.Is(err, ErrSessionOwnership) {
		t.Fatalf("expected ErrSessionOwnership for old document, got: %v", err)
	}

	// Tab-2 is active owner and writes with generation 2 succeed
	gen2 := uint64(2)
	newDocScope := CommandScope{
		User:              "admin",
		SessionID:         "s1",
		ClientID:          "tab-2",
		BindingGeneration: &gen2,
		OperationID:       "g2:handoff-write",
	}
	if st, err := h.server.ExecuteCommand(h.ctx, newDocScope, PlaybackCommand{Action: CommandGain, Gain: 0.1}); err != nil || st.Gain != 0.1 {
		t.Fatalf("new document command failed: %v", err)
	}
}

func newMigrationRepairFixture(t *testing.T) (*durableScenarioHarness, *playbackDevice, ReserveMigrationRequest, CommitMigrationRequest) {
	t.Helper()
	h := newDurableScenarioHarness(t)
	source := h.attach(t, "source-owner")
	target := NewPlaybackDevice(h.ctx, h.server, "Target", "target")
	target.PlaybackQueue.Set(model.MediaFiles{{ID: "target-original", Path: "/target.mp3"}})
	h.server.playbackDevices = append(h.server.playbackDevices, target)
	if _, err := h.server.AttachSession(h.ctx, AttachRequest{SessionID: "target-session", User: "other-user", ClientID: "target-owner", DeviceName: "target"}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"migrationId": "repair-migration", "operationId": "g1:reserve", "sessionId": "s1",
		"clientId": "source-owner", "deviceName": "target", "bindingGeneration": source.BindingGeneration,
		"forceTakeover":       true,
		"expectedFingerprint": target.SceneFingerprint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var reserve ReserveMigrationRequest
	if err := json.Unmarshal(payload, &reserve); err != nil {
		t.Fatal(err)
	}
	reserve.User = "admin"
	commit := CommitMigrationRequest{
		MigrationID: reserve.MigrationID, OperationID: "g1:commit", SessionID: "s1", ClientID: "source-owner", User: "admin",
		TargetDeviceName: "target", ExpectedGeneration: source.BindingGeneration,
	}
	return h, target, reserve, commit
}

func TestDurableSessionMigrationRepair(t *testing.T) {
	t.Run("reserve validates source owner before target resolution", func(t *testing.T) {
		h, target, reserve, _ := newMigrationRepairFixture(t)
		reserve.ClientID = "observer"
		if _, err := h.server.ReserveMigration(h.ctx, reserve); !errors.Is(err, ErrSessionOwnership) {
			t.Fatalf("non-owner reserve must fail before mutation: %v", err)
		}
		if target.PlaybackQueue.Size() != 1 || len(h.server.reservations) != 0 {
			t.Fatal("non-owner reservation changed target or reservation state")
		}
	})
	for _, missing := range []string{"client", "generation"} {
		t.Run("commit rejects missing "+missing, func(t *testing.T) {
			h, target, reserve, commit := newMigrationRepairFixture(t)
			if _, err := h.server.ReserveMigration(h.ctx, reserve); err != nil {
				t.Fatal(err)
			}
			if missing == "client" {
				commit.ClientID = ""
			} else {
				commit.ExpectedGeneration = 0
			}
			if _, err := h.server.CommitMigration(h.ctx, commit); err == nil {
				t.Fatal("missing protected-write scope committed migration")
			}
			if target.PlaybackQueue.Size() != 1 {
				t.Fatal("rejected migration cleared the occupied target")
			}
		})
	}
	t.Run("omitted commit fingerprint still detects changed target", func(t *testing.T) {
		h, target, reserve, commit := newMigrationRepairFixture(t)
		if _, err := h.server.ReserveMigration(h.ctx, reserve); err != nil {
			t.Fatal(err)
		}
		target.mu.Lock()
		target.PlaybackQueue.Set(model.MediaFiles{{ID: "third-party", Path: "/new.mp3"}})
		target.incrementSceneRevisionLocked()
		target.mu.Unlock()
		if _, err := h.server.CommitMigration(h.ctx, commit); err == nil {
			t.Fatal("omitting request fingerprint bypassed reserved scene validation")
		}
		if target.PlaybackQueue.Size() != 1 || target.PlaybackQueue.Items[0].ID != "third-party" {
			t.Fatal("commit destroyed a newer third-party scene")
		}
	})
	t.Run("occupied target old controller is revoked at commit", func(t *testing.T) {
		h, target, reserve, commit := newMigrationRepairFixture(t)
		if _, err := h.server.ReserveMigration(h.ctx, reserve); err != nil {
			t.Fatal(err)
		}
		if _, err := h.server.CommitMigration(h.ctx, commit); err != nil {
			t.Fatal(err)
		}
		oldGeneration := uint64(1)
		old := CommandScope{User: "other-user", SessionID: "target-session", ClientID: "target-owner", BindingGeneration: &oldGeneration, OperationID: "g1:former-owner-write"}
		before := target.Gain
		if _, err := h.server.ExecuteCommand(h.ctx, old, PlaybackCommand{Action: CommandGain, Gain: .123}); err == nil {
			t.Fatal("former target controller remained authorized after commit")
		}
		if target.Gain != before {
			t.Fatal("former controller changed imported target gain")
		}
	})
	t.Run("migration commit is queryable after response loss", func(t *testing.T) {
		h, _, reserve, commit := newMigrationRepairFixture(t)
		if _, err := h.server.ReserveMigration(h.ctx, reserve); err != nil {
			t.Fatal(err)
		}
		if _, err := h.server.CommitMigration(h.ctx, commit); err != nil {
			t.Fatal(err)
		}
		rec, err := h.server.QueryOperation(h.ctx, commit.SessionID, commit.OperationID)
		if err != nil || rec.Status != OperationStatusApplied {
			t.Fatalf("lost commit response cannot reconcile original operation: %#v %v", rec, err)
		}
	})
	t.Run("explicit zero gain position and false mute are imported", func(t *testing.T) {
		h, target, reserve, commit := newMigrationRepairFixture(t)
		target.Gain = .9
		target.DesiredGain = .9
		target.Muted = true
		if _, err := h.server.ReserveMigration(h.ctx, reserve); err != nil {
			t.Fatal(err)
		}
		zeroGain := float32(0)
		zeroPosition := 0
		muted := false
		commit.Gain = &zeroGain
		commit.Position = &zeroPosition
		commit.Muted = &muted
		if _, err := h.server.CommitMigration(h.ctx, commit); err != nil {
			t.Fatal(err)
		}
		if target.Gain != 0 || target.DesiredGain != 0 || target.recoveryPosition == nil || *target.recoveryPosition != 0 || target.Muted {
			t.Fatalf("explicit zero/false migration values were not preserved: gain=%v desired=%v position=%v muted=%v", target.Gain, target.DesiredGain, target.recoveryPosition, target.Muted)
		}
	})
}

func TestDurableSessionOperationRepair(t *testing.T) {
	t.Run("failed command retries retain the original typed error", func(t *testing.T) {
		h := newDurableScenarioHarness(t)
		h.attach(t, "owner")
		failure := errors.New("pause IPC rejected")
		track := &mockTrack{playing: true, onPause: func() error { return failure }}
		h.device.ActiveTrack = track
		generation := uint64(1)
		scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "owner", BindingGeneration: &generation, OperationID: "g1:failed-pause"}
		for i := 0; i < 2; i++ {
			if _, err := h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandPause}); !errors.Is(err, failure) {
				t.Fatalf("attempt %d changed failure into success or lost error identity: %v", i, err)
			}
		}
		if track.pauseCalls != 1 {
			t.Fatalf("failed retry executed %d times", track.pauseCalls)
		}
	})
	t.Run("execution timeout is unknown", func(t *testing.T) {
		h := newDurableScenarioHarness(t)
		h.attach(t, "owner")
		h.device.ActiveTrack = &mockTrack{playing: true, onPause: func() error { return context.DeadlineExceeded }}
		generation := uint64(1)
		scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "owner", BindingGeneration: &generation, OperationID: "g1:unknown-pause"}
		_, _ = h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandPause})
		rec, err := h.server.QueryOperation(h.ctx, "s1", scope.OperationID)
		if err != nil || rec.Status != OperationStatusUnknown {
			t.Fatalf("timeout fabricated definitive failure: %#v %v", rec, err)
		}
	})
	for _, eviction := range []string{"ttl", "capacity"} {
		t.Run(eviction+" eviction does not permit replay", func(t *testing.T) {
			h := newDurableScenarioHarness(t)
			h.attach(t, "owner")
			h.server.operationStore = NewOperationStore(1, time.Minute, func() time.Time { return h.now })
			generation := uint64(1)
			scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "owner", BindingGeneration: &generation, OperationID: "g1:original"}
			if _, err := h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandGain, Gain: .4}); err != nil {
				t.Fatal(err)
			}
			if eviction == "ttl" {
				h.advance(2 * time.Minute)
			} else {
				other := scope
				other.OperationID = "g1:second"
				if _, err := h.server.ExecuteCommand(h.ctx, other, PlaybackCommand{Action: CommandGain, Gain: .6}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandGain, Gain: .8}); err == nil {
				t.Fatal("indeterminate operation was replayed after history eviction")
			}
		})
	}
	t.Run("cached result cannot bypass generation checks", func(t *testing.T) {
		h := newDurableScenarioHarness(t)
		h.attach(t, "owner")
		generation := uint64(1)
		scope := CommandScope{User: "admin", SessionID: "s1", ClientID: "owner", BindingGeneration: &generation, OperationID: "g1:cached"}
		if _, err := h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandGain, Gain: .4}); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []*uint64{nil, new(uint64), func() *uint64 { v := uint64(2); return &v }()} {
			scope.BindingGeneration = invalid
			if _, err := h.server.ExecuteCommand(h.ctx, scope, PlaybackCommand{Action: CommandGain, Gain: .8}); err == nil {
				t.Fatal("cached operation bypassed generation validation")
			}
		}
	})
}
