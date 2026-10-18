package playback

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockTrack struct {
	playing          bool
	pauseCalls       int
	closeCalls       int
	position         int
	positionSetCalls int
	volumeValues     []float32
	onSetVolume      func(float32)
	onUnpause        func()
	onClose          func()
}

func (m *mockTrack) IsPlaying() bool { return m.playing }
func (m *mockTrack) SetVolume(value float32) {
	m.volumeValues = append(m.volumeValues, value)
	if m.onSetVolume != nil {
		m.onSetVolume(value)
	}
}
func (m *mockTrack) Pause() {
	m.pauseCalls++
	m.playing = false
}
func (m *mockTrack) Unpause() {
	m.playing = true
	if m.onUnpause != nil {
		m.onUnpause()
	}
}
func (m *mockTrack) Position() int { return m.position }
func (m *mockTrack) SetPosition(offset int) error {
	m.positionSetCalls++
	m.position = offset
	return nil
}
func (m *mockTrack) Close() {
	m.closeCalls++
	if m.onClose != nil {
		m.onClose()
	}
}
func (m *mockTrack) String() string { return "mockTrack" }

func TestPlaybackDeviceFailureKeepsQueuePositionAndRejectsRetiredToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/same.mp3"}, {ID: "2", Path: "/next.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1", "1", "2"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	type preparedTrack struct {
		token model.PlaybackToken
		track *mockTrack
	}
	prepared := make(chan preparedTrack, 2)
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		track := &mockTrack{}
		prepared <- preparedTrack{token: token, track: track}
		return track, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		newTrack = previousNewTrack
	})

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	first := <-prepared
	position := 17
	device.PlaybackEvents <- model.PlaybackEvent{
		Token:    first.token,
		Kind:     model.PlaybackEventFailure,
		Position: &position,
		Err:      errors.New("IPC disconnected"),
	}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex: 0,
		Playing:      false,
		Gain:         DefaultGain,
		Position:     position,
		QueueVersion: 1,
	})
	if first.track.closeCalls != 1 {
		t.Fatalf("failed output was not released: closes=%d", first.track.closeCalls)
	}

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("resume after output failure: status=%#v, err=%v", status, err)
	}
	second := <-prepared
	if second.token == first.token {
		t.Fatalf("reused playback token for repeated media ID: %d", second.token)
	}
	if second.track.position != position {
		t.Fatalf("recovery position: got=%d, want=%d", second.track.position, position)
	}

	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventNaturalEnd}
	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventFailure, Err: errors.New("late fault")}
	assertDeviceStatusStable(t, device, ctx, DeviceStatus{
		CurrentIndex: 0,
		Playing:      true,
		Gain:         DefaultGain,
		Position:     position,
		QueueVersion: 1,
	}, 100*time.Millisecond)
	if second.track.closeCalls != 0 {
		t.Fatalf("stale event closed successor output: closes=%d", second.track.closeCalls)
	}

	retiredPosition := 20
	device.PlaybackEvents <- model.PlaybackEvent{
		Token:    second.token,
		Kind:     model.PlaybackEventRetired,
		Position: &retiredPosition,
	}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex: 0,
		Playing:      false,
		Gain:         DefaultGain,
		Position:     retiredPosition,
		QueueVersion: 1,
	})
	if second.track.closeCalls != 1 {
		t.Fatalf("retired output was not released: closes=%d", second.track.closeCalls)
	}
}

func TestPlaybackDeviceUnknownFailurePositionDoesNotInventSeekOffset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	type preparedOutput struct {
		token model.PlaybackToken
		track *mockTrack
	}
	prepared := make(chan preparedOutput, 2)
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		track := &mockTrack{}
		prepared <- preparedOutput{token: token, track: track}
		return track, nil
	}
	t.Cleanup(func() {
		_, _ = device.Shutdown(ctx)
		cancel()
		newTrack = previousNewTrack
	})

	if _, err := device.Start(ctx); err != nil {
		t.Fatalf("initial start: %v", err)
	}
	first := <-prepared
	device.PlaybackEvents <- model.PlaybackEvent{Token: first.token, Kind: model.PlaybackEventFailure, Err: errors.New("position unavailable")}
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex: 0,
		Playing:      false,
		Gain:         DefaultGain,
		Position:     0,
		QueueVersion: 1,
	})
	if first.track.closeCalls != 1 {
		t.Fatalf("failed output was not released: closes=%d", first.track.closeCalls)
	}

	if _, err := device.Start(ctx); err != nil {
		t.Fatalf("resume after output failure: %v", err)
	}
	second := <-prepared
	if second.track.positionSetCalls != 0 {
		t.Fatalf("unknown position was fabricated as a seek target: calls=%d", second.track.positionSetCalls)
	}
}

func waitDeviceStatus(t *testing.T, device *playbackDevice, ctx context.Context, expected DeviceStatus) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatalf("read playback status: %v", err)
		}
		if status == expected {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("playback status did not become %#v; got %#v", expected, status)
		}
	}
}

func assertDeviceStatusStable(t *testing.T, device *playbackDevice, ctx context.Context, expected DeviceStatus, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := device.Status(ctx)
		if err != nil {
			t.Fatalf("read playback status: %v", err)
		}
		if status != expected {
			t.Fatalf("playback status changed: expected %#v, got %#v", expected, status)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return
		}
	}
}

func TestPlaybackDeviceInvokesStateChangeCallbackOnSkip(t *testing.T) {
	called := make(chan DeviceStatus, 1)
	pd := &playbackDevice{
		PlaybackQueue:  NewQueue(),
		PlaybackEvents: make(chan model.PlaybackEvent, 16),
		ActiveTrack:    &mockTrack{},
		onStateChange:  func(status DeviceStatus) { called <- status },
	}
	pd.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})

	status, err := pd.Skip(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("skip failed: %v", err)
	}

	select {
	case got := <-called:
		if got != status {
			t.Fatalf("expected callback status %#v, got %#v", status, got)
		}
	default:
		t.Fatal("expected state change callback to be invoked")
	}
}

type pendingPlaybackResult struct {
	status DeviceStatus
	err    error
}

type playbackEventEmitter struct {
	events chan<- model.PlaybackEvent
	token  model.PlaybackToken
}

func (e playbackEventEmitter) emit(kind model.PlaybackEventKind, position *int, err error) {
	e.events <- model.PlaybackEvent{Token: e.token, Kind: kind, Position: position, Err: err}
}

type pendingPlaybackHarness struct {
	ctx        context.Context
	device     *playbackDevice
	track      *mockTrack
	successor  *mockTrack
	entered    chan struct{}
	resolved   chan struct{}
	playedGain chan float32
	playedIDs  chan string
	ended      chan playbackEventEmitter
	release    func()
}

func newPendingPlaybackHarness(t *testing.T, blockedID string, ids []string) *pendingPlaybackHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}, {ID: "2", Path: "/b.mp3"}, {ID: "3", Path: "/c.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	h := &pendingPlaybackHarness{
		ctx:        ctx,
		device:     NewPlaybackDevice(ctx, server, "Speaker", "pulse/test"),
		track:      &mockTrack{},
		successor:  &mockTrack{},
		entered:    make(chan struct{}),
		resolved:   make(chan struct{}),
		playedGain: make(chan float32, 1),
		playedIDs:  make(chan string, 3),
		ended:      make(chan playbackEventEmitter, 1),
	}
	if _, err := h.device.Set(ctx, ids); err != nil {
		t.Fatalf("set queue: %v", err)
	}
	release := make(chan struct{})
	var releaseOnce, resolvedOnce, playedOnce sync.Once
	h.release = func() { releaseOnce.Do(func() { close(release) }) }
	h.track.onClose = func() { resolvedOnce.Do(func() { close(h.resolved) }) }
	h.track.onUnpause = func() {
		playedOnce.Do(func() {
			gain := float32(0)
			if len(h.track.volumeValues) > 0 {
				gain = h.track.volumeValues[len(h.track.volumeValues)-1]
			}
			h.playedGain <- gain
		})
		resolvedOnce.Do(func() { close(h.resolved) })
	}
	observe := func(output *mockTrack, id string) *mockTrack {
		onUnpause := output.onUnpause
		output.onUnpause = func() {
			if onUnpause != nil {
				onUnpause()
			}
			h.playedIDs <- id
		}
		return output
	}
	previousNewTrack := newTrack
	blocked := false
	initial := &mockTrack{}
	newTrack = func(_ context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, mf model.MediaFile) (Track, error) {
		if mf.ID == blockedID {
			if !blocked {
				blocked = true
				output := observe(h.track, mf.ID)
				close(h.entered)
				<-release
				return output, nil
			}
			return observe(h.successor, mf.ID), nil
		}
		h.ended <- playbackEventEmitter{events: events, token: token}
		return observe(initial, mf.ID), nil
	}
	t.Cleanup(func() {
		_, _ = h.device.Shutdown(ctx)
		h.release()
		select {
		case <-h.entered:
			select {
			case <-h.resolved:
			case <-time.After(time.Second):
				t.Error("pending output did not resolve during cleanup")
			}
		default:
		}
		cancel()
		newTrack = previousNewTrack
	})
	return h
}

func (h *pendingPlaybackHarness) wait(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func (h *pendingPlaybackHarness) begin(t *testing.T, action func(context.Context) (DeviceStatus, error)) <-chan pendingPlaybackResult {
	t.Helper()
	done := make(chan pendingPlaybackResult, 1)
	go func() {
		status, err := action(h.ctx)
		done <- pendingPlaybackResult{status: status, err: err}
	}()
	h.wait(t, h.entered, "playback did not begin preparing the output")
	return done
}

func (h *pendingPlaybackHarness) finish(t *testing.T, done <-chan pendingPlaybackResult) DeviceStatus {
	t.Helper()
	h.release()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("preparation completion: %v", result.err)
		}
		return result.status
	case <-time.After(time.Second):
		t.Fatal("playback did not finish after releasing the output")
		return DeviceStatus{}
	}
}

func (h *pendingPlaybackHarness) assertRetired(t *testing.T, playing bool, ids []string) {
	t.Helper()
	queue, status, err := h.device.Get(h.ctx)
	if err != nil || status.Playing != playing {
		t.Fatalf("cancelled preparation changed playback: status=%#v, err=%v", status, err)
	}
	if h.track.playing || h.track.closeCalls != 1 {
		t.Fatalf("cancelled output was not retired: playing=%v, closes=%d", h.track.playing, h.track.closeCalls)
	}
	if playing && (!h.successor.playing || h.successor.closeCalls != 0) {
		t.Fatalf("cancelled output displaced successor: playing=%v, closes=%d", h.successor.playing, h.successor.closeCalls)
	}
	if len(queue) != len(ids) {
		t.Fatalf("unexpected authoritative queue length: got=%d, want=%d", len(queue), len(ids))
	}
	for i, id := range ids {
		if queue[i].ID != id {
			t.Fatalf("unexpected authoritative queue at %d: got=%s, want=%s", i, queue[i].ID, id)
		}
	}
}

func testPlaybackDeviceCancelsPendingStart(t *testing.T, stop func(*playbackDevice, context.Context) (DeviceStatus, error), resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	stopped, err := stop(h.device, h.ctx)
	if err != nil || stopped.Playing {
		t.Fatalf("stop while preparing: status=%#v, err=%v", stopped, err)
	}
	if resume {
		resumed, err := h.device.Start(h.ctx)
		if err != nil || !resumed.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", resumed, err)
		}
	}
	if status := h.finish(t, done); status.Playing != resume {
		t.Fatalf("cancelled start returned the wrong playback state: %#v", status)
	}
	h.assertRetired(t, resume, []string{"1"})
}

func TestPlaybackDeviceClearCancelsPendingStart(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Clear(h.ctx); err != nil {
		t.Fatalf("clear while preparing: %v", err)
	}
	h.finish(t, done)
	h.assertRetired(t, false, nil)
}

func TestPlaybackDeviceSetCancelsPendingStart(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Set(h.ctx, []string{"3"}); err != nil {
		t.Fatalf("replace queue while preparing: %v", err)
	}
	h.finish(t, done)
	h.assertRetired(t, false, []string{"3"})
}

func testPlaybackDeviceCancelsPendingSkip(t *testing.T, stop func(*playbackDevice, context.Context) (DeviceStatus, error), resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "2", []string{"1", "2"})
	if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	done := h.begin(t, func(ctx context.Context) (DeviceStatus, error) { return h.device.Skip(ctx, 1, 0) })
	if status, err := stop(h.device, h.ctx); err != nil || status.Playing {
		t.Fatalf("stop while skipping: status=%#v, err=%v", status, err)
	}
	if resume {
		if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", status, err)
		}
	}
	if status := h.finish(t, done); status.Playing != resume {
		t.Fatalf("cancelled skip returned the wrong playback state: %#v", status)
	}
	h.assertRetired(t, resume, []string{"1", "2"})
}

func TestPlaybackDeviceStopCancelsPendingSkip(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Stop, false)
}

func TestPlaybackDeviceShutdownCancelsPendingSkip(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Shutdown, false)
}

func testPlaybackDeviceCancelsPendingAdvance(t *testing.T, resume bool) {
	t.Helper()
	h := newPendingPlaybackHarness(t, "2", []string{"1", "2"})
	if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	var ended playbackEventEmitter
	select {
	case ended = <-h.ended:
	case <-time.After(time.Second):
		t.Fatal("initial output did not expose its natural-end event")
	}
	ended.emit(model.PlaybackEventNaturalEnd, nil, nil)
	h.wait(t, h.entered, "natural advance did not begin preparing the next output")
	if status, err := h.device.Stop(h.ctx); err != nil || status.Playing {
		t.Fatalf("stop while advancing: status=%#v, err=%v", status, err)
	}
	if resume {
		if status, err := h.device.Start(h.ctx); err != nil || !status.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", status, err)
		}
	}
	h.release()
	h.wait(t, h.resolved, "natural-advance output did not resolve")
	h.assertRetired(t, resume, []string{"1", "2"})
}

func TestPlaybackDeviceStopCancelsPendingNaturalAdvance(t *testing.T) {
	testPlaybackDeviceCancelsPendingAdvance(t, false)
}

func TestPlaybackDeviceCancelledSkipDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingSkip(t, (*playbackDevice).Stop, true)
}

func TestPlaybackDeviceCancelledNaturalAdvanceDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingAdvance(t, true)
}

func TestPlaybackDevicePendingStartUsesLatestGainBeforeUnpause(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1"})
	done := h.begin(t, h.device.Start)
	if status, err := h.device.SetGain(h.ctx, 0.37); err != nil || status.Gain != float32(0.37) {
		t.Fatalf("gain while preparing: status=%#v, err=%v", status, err)
	}
	if status := h.finish(t, done); !status.Playing || status.Gain != float32(0.37) {
		t.Fatalf("start did not retain the latest gain: %#v", status)
	}
	select {
	case gain := <-h.playedGain:
		if gain != float32(0.37) {
			t.Fatalf("first playback used stale gain: %v", gain)
		}
	case <-time.After(time.Second):
		t.Fatal("output did not expose its gain at first playback")
	}
}

func TestPlaybackDeviceRemoveCancelsPreparationForCurrentItem(t *testing.T) {
	h := newPendingPlaybackHarness(t, "1", []string{"1", "2"})
	done := h.begin(t, h.device.Start)
	if _, err := h.device.Remove(h.ctx, 0); err != nil {
		t.Fatalf("remove current item while preparing: %v", err)
	}
	h.finish(t, done)
	queue, status, err := h.device.Get(h.ctx)
	if err != nil || len(queue) != 1 || queue[0].ID != "2" {
		t.Fatalf("remove did not retain the next queue item: length=%d, status=%#v, err=%v", len(queue), status, err)
	}
	select {
	case playedID := <-h.playedIDs:
		t.Fatalf("deleted prepared content started: output=%s, current queue item=%s", playedID, queue[0].ID)
	default:
	}
	h.assertRetired(t, false, []string{"2"})
	if resumed, err := h.device.Start(h.ctx); err != nil || !resumed.Playing {
		t.Fatalf("start retained queue item: status=%#v, err=%v", resumed, err)
	}
	queue, status, err = h.device.Get(h.ctx)
	if err != nil || status.CurrentIndex < 0 || status.CurrentIndex >= len(queue) {
		t.Fatalf("retained item status: status=%#v, length=%d, err=%v", status, len(queue), err)
	}
	select {
	case playedID := <-h.playedIDs:
		if playedID != "2" || playedID != queue[status.CurrentIndex].ID {
			t.Fatalf("output differs from current queue: output=%s, queue=%s", playedID, queue[status.CurrentIndex].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("retained item did not reach the output")
	}
}

func TestPlaybackDeviceStopCancelsPendingStart(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Stop, false)
}

func TestPlaybackDeviceShutdownCancelsPendingStart(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Shutdown, false)
}

func TestPlaybackDeviceCancelledStartDoesNotDisplaceSuccessor(t *testing.T) {
	testPlaybackDeviceCancelsPendingStart(t, (*playbackDevice).Stop, true)
}

func TestPlaybackDeviceNaturalAdvanceRetriesTransientErrorAndSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mediaFiles := map[string]model.MediaFile{
		"1": {ID: "1", Path: "/1.mp3"},
		"2": {ID: "2", Path: "/2.mp3"},
	}
	stub := &sessionStatusStubServer{mediaFiles: mediaFiles}
	device := NewPlaybackDevice(ctx, stub, "Speaker", "auto")
	if _, err := device.Add(ctx, []string{"1", "2"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	previousNewTrack := newTrack
	attempts := 0
	newTrack = func(ctx context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, deviceName string, mf model.MediaFile) (Track, error) {
		if mf.ID == "2" {
			attempts++
			if attempts == 1 {
				return nil, errors.New("transient PipeWire link error")
			}
		}
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("initial start failed: %#v, %v", status, err)
	}

	// Trigger natural end of track 1
	device.PlaybackEvents <- model.PlaybackEvent{
		Token: device.activePlaybackToken,
		Kind:  model.PlaybackEventNaturalEnd,
	}

	// Wait for retry to succeed and device to advance to track 2
	waitDeviceStatus(t, device, ctx, DeviceStatus{
		CurrentIndex: 1,
		Playing:      true,
		Gain:         DefaultGain,
		Position:     0,
		QueueVersion: 1,
	})
	if attempts < 2 {
		t.Fatalf("expected at least 2 attempts for track 2 (retry), got %d", attempts)
	}
}

func TestPlaybackDeviceNaturalAdvanceReportsFailureAfterRetriesExhausted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mediaFiles := map[string]model.MediaFile{
		"1": {ID: "1", Path: "/1.mp3"},
		"2": {ID: "2", Path: "/2.mp3"},
	}
	stub := &sessionStatusStubServer{mediaFiles: mediaFiles}
	device := NewPlaybackDevice(ctx, stub, "Speaker", "auto")
	if _, err := device.Add(ctx, []string{"1", "2"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	previousNewTrack := newTrack
	newTrack = func(ctx context.Context, events chan<- model.PlaybackEvent, token model.PlaybackToken, deviceName string, mf model.MediaFile) (Track, error) {
		if mf.ID == "2" {
			return nil, errors.New("persistent output error")
		}
		return &mockTrack{playing: true}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("initial start failed: %#v, %v", status, err)
	}

	// Trigger natural end of track 1
	device.PlaybackEvents <- model.PlaybackEvent{
		Token: device.activePlaybackToken,
		Kind:  model.PlaybackEventNaturalEnd,
	}

	// Wait for retries to be exhausted and device to report failure
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := device.Status(ctx)
		if err == nil && status.CurrentIndex == 1 && !status.Playing && device.FailureReason() == "track_switch_failed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, _ := device.Status(ctx)
	t.Fatalf("expected track_switch_failed after retries exhausted, got: %#v, reason=%s", status, device.FailureReason())
}

var _ = Describe("playbackDevice concurrency", func() {
	It("handles concurrent Status and Clear calls without panic", func() {
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
		}

		ctx := context.Background()
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _ = pd.Status(ctx)
			}()
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _ = pd.Clear(ctx)
			}()
		}
		wg.Wait()
		Expect(true).To(BeTrue()) // reaching here = no panic/race
	})

	It("Stop pauses but keeps the active track resumable", func() {
		track := &mockTrack{playing: true, position: 17}
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Stop(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(track.pauseCalls).To(Equal(1))
		Expect(track.closeCalls).To(Equal(0))
		Expect(pd.ActiveTrack).To(BeIdenticalTo(track))
		Expect(status.Playing).To(BeFalse())
	})

	It("Shutdown pauses, closes, and releases the active track", func() {
		track := &mockTrack{playing: true, position: 23}
		pd := &playbackDevice{
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Shutdown(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(track.pauseCalls).To(Equal(1))
		Expect(track.closeCalls).To(Equal(1))
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(status.Playing).To(BeFalse())
		Expect(status.Position).To(Equal(0))
	})
})

// ---------------------------------------------------------------------------
// Adversarial regression tests for natural-end retry backoff (device.go:552-565).
// ---------------------------------------------------------------------------

func newAdversarialDevice(t *testing.T, onTwo func(attempt int) error) (context.Context, context.CancelFunc, *playbackDevice, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mediaFiles := map[string]model.MediaFile{
		"1": {ID: "1", Path: "/1.mp3"},
		"2": {ID: "2", Path: "/2.mp3"},
	}
	stub := &sessionStatusStubServer{mediaFiles: mediaFiles}
	device := NewPlaybackDevice(ctx, stub, "Speaker", "auto")
	if _, err := device.Add(ctx, []string{"1", "2"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	var attempts atomic.Int32
	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, mf model.MediaFile) (Track, error) {
		if mf.ID == "2" {
			attempt := int(attempts.Add(1))
			if err := onTwo(attempt); err != nil {
				return nil, err
			}
		}
		return &mockTrack{}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })
	if status, err := device.Start(ctx); err != nil || !status.Playing {
		t.Fatalf("initial start: status=%#v, err=%v", status, err)
	}
	return ctx, cancel, device, &attempts
}

func waitAdversarialRetryBackoff(t *testing.T, device *playbackDevice, attempts *atomic.Int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		device.mu.Lock()
		ready := device.PlaybackQueue.Index == 1 && device.ActiveTrack == nil && len(device.pendingPlaybackTokens) == 0 && attempts.Load() == 1
		device.mu.Unlock()
		if ready {
			return
		}
		if attempts.Load() > 1 {
			t.Fatal("missed the first retry backoff window")
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("first failed preparation did not enter retry backoff")
}

func emitAdversarialNaturalEnd(device *playbackDevice) {
	device.PlaybackEvents <- model.PlaybackEvent{
		Token: device.activePlaybackToken,
		Kind:  model.PlaybackEventNaturalEnd,
	}
}

func TestAdversarialRetryBackoffDeadlocksOnContextCancel(t *testing.T) {
	firstFailure := make(chan struct{})
	var failOnce atomic.Int32
	ctx, cancel, device, attempts := newAdversarialDevice(t, func(attempt int) error {
		if failOnce.CompareAndSwap(0, 1) {
			close(firstFailure)
		}
		return errors.New("persistent output error")
	})
	t.Cleanup(cancel)

	emitAdversarialNaturalEnd(device)
	select {
	case <-firstFailure:
	case <-time.After(2 * time.Second):
		t.Fatal("first retry failure did not happen; backoff never entered")
	}
	waitAdversarialRetryBackoff(t, device, attempts)
	cancel()
	time.Sleep(100 * time.Millisecond)

	type statusResult struct {
		status DeviceStatus
		err    error
	}
	done := make(chan statusResult, 1)
	go func() {
		status, err := device.Status(context.Background())
		done <- statusResult{status: status, err: err}
	}()
	select {
	case result := <-done:
		t.Logf("Status returned after cancel during backoff: %#v, err=%v (no deadlock)", result.status, result.err)
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: device.Status blocked >2s after context cancel during natural-end retry backoff; " +
			"trackSwitcher self-locked pd.mu at device.go:557-561 (Lock inside Done() branch, then Lock again after select)")
	}
	_ = ctx
}

func TestAdversarialStopDuringBackoffRestartsAudio(t *testing.T) {
	firstFailure := make(chan struct{})
	var failOnce atomic.Int32
	ctx, cancel, device, attempts := newAdversarialDevice(t, func(attempt int) error {
		if attempt == 1 {
			if failOnce.CompareAndSwap(0, 1) {
				close(firstFailure)
			}
			return errors.New("transient PipeWire link error")
		}
		return nil
	})
	t.Cleanup(func() {
		cancel()
		_, _ = device.Shutdown(context.Background())
	})

	emitAdversarialNaturalEnd(device)
	select {
	case <-firstFailure:
	case <-time.After(2 * time.Second):
		t.Fatal("first retry failure did not happen; backoff never entered")
	}

	waitAdversarialRetryBackoff(t, device, attempts)
	stopped, err := device.Stop(ctx)
	if err != nil || stopped.Playing {
		t.Fatalf("stop during backoff: status=%#v, err=%v", stopped, err)
	}

	time.Sleep(600 * time.Millisecond)

	if got := attempts.Load(); got > 1 {
		status, _ := device.Status(context.Background())
		t.Fatalf("Stop during retry backoff did not cancel the retry: newTrack attempts=%d, status=%#v; "+
			"guard at device.go:562 checks Index but not preparationVersion, so audio restarted after Stop",
			got, status)
	}
	status, err := device.Status(context.Background())
	if err != nil {
		t.Fatalf("status after stop: %v", err)
	}
	if status.Playing {
		t.Fatalf("audio is playing after Stop during retry backoff: %#v (stale retry passed device.go:562 guard)",
			status)
	}
}

func TestAdversarialManualStartLeavesStaleFailureReason(t *testing.T) {
	ctx, cancel, device, attempts := newAdversarialDevice(t, func(attempt int) error {
		return errors.New("persistent output error")
	})
	t.Cleanup(func() {
		cancel()
		_, _ = device.Shutdown(context.Background())
	})

	emitAdversarialNaturalEnd(device)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if device.FailureReason() == "track_switch_failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reason := device.FailureReason(); reason != "track_switch_failed" {
		t.Fatalf("expected retries to be exhausted with track_switch_failed, got %q (attempts=%d)", reason, attempts.Load())
	}

	previousNewTrack := newTrack
	newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
		return &mockTrack{}, nil
	}
	t.Cleanup(func() { newTrack = previousNewTrack })

	status, err := device.Start(ctx)
	if err != nil || !status.Playing {
		t.Fatalf("manual start after repair: status=%#v, err=%v", status, err)
	}
	if reason := device.FailureReason(); reason != "" {
		t.Fatalf("manual Start succeeded but failureReason was not cleared: %q (startLocked never resets it)", reason)
	}
}

func TestAdversarialShutdownDuringBackoffCancelsRetry(t *testing.T) {
	firstFailure := make(chan struct{})
	var failOnce atomic.Int32
	ctx, cancel, device, attempts := newAdversarialDevice(t, func(attempt int) error {
		if attempt == 1 {
			if failOnce.CompareAndSwap(0, 1) {
				close(firstFailure)
			}
			return errors.New("transient error")
		}
		return nil
	})
	t.Cleanup(func() {
		cancel()
		_, _ = device.Shutdown(context.Background())
	})

	emitAdversarialNaturalEnd(device)
	select {
	case <-firstFailure:
	case <-time.After(2 * time.Second):
		t.Fatal("first retry failure did not happen; backoff never entered")
	}

	waitAdversarialRetryBackoff(t, device, attempts)
	shutDownStatus, err := device.Shutdown(ctx)
	if err != nil || shutDownStatus.Playing {
		t.Fatalf("shutdown during backoff: status=%#v, err=%v", shutDownStatus, err)
	}

	time.Sleep(600 * time.Millisecond)

	if got := attempts.Load(); got > 1 {
		t.Fatalf("Shutdown during retry backoff did not cancel retry: attempts=%d", got)
	}
	status, err := device.Status(context.Background())
	if err != nil {
		t.Fatalf("status after shutdown: %v", err)
	}
	if status.Playing {
		t.Fatalf("audio playing after Shutdown during retry backoff: %#v", status)
	}
}
