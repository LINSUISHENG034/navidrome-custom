package playback

import (
	"context"
	"errors"
	"sync"
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
	muted            bool
	muteCalls        int
	onSetMute        func(bool) error
	onSetVolume      func(float32) error
	onPause          func() error
	onUnpause        func() error
	onClose          func()
}

func (m *mockTrack) IsPlaying() bool { return m.playing }
func (m *mockTrack) SetVolume(value float32) error {
	m.volumeValues = append(m.volumeValues, value)
	if m.onSetVolume != nil {
		return m.onSetVolume(value)
	}
	return nil
}
func (m *mockTrack) SetMute(value bool) error {
	m.muteCalls++
	m.muted = value
	if m.onSetMute != nil {
		return m.onSetMute(value)
	}
	return nil
}
func (m *mockTrack) Pause() error {
	m.pauseCalls++
	if m.onPause != nil {
		if err := m.onPause(); err != nil {
			return err
		}
	}
	m.playing = false
	return nil
}
func (m *mockTrack) Unpause() error {
	if m.onUnpause != nil {
		if err := m.onUnpause(); err != nil {
			return err
		}
	}
	m.playing = true
	return nil
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
		DesiredGain:  DefaultGain,
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
		DesiredGain:  DefaultGain,
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
		DesiredGain:  DefaultGain,
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
		DesiredGain:  DefaultGain,
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
	h.track.onUnpause = func() error {
		playedOnce.Do(func() {
			gain := float32(0)
			if len(h.track.volumeValues) > 0 {
				gain = h.track.volumeValues[len(h.track.volumeValues)-1]
			}
			h.playedGain <- gain
		})
		resolvedOnce.Do(func() { close(h.resolved) })
		return nil
	}
	observe := func(output *mockTrack, id string) *mockTrack {
		onUnpause := output.onUnpause
		output.onUnpause = func() error {
			if onUnpause != nil {
				_ = onUnpause()
			}
			h.playedIDs <- id
			return nil
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

	It("SetGain propagates synchronous IPC error, retains desired gain, but keeps confirmed gain unchanged", func() {
		errIPC := errors.New("mpv ipc: set_property volume failed")
		track := &mockTrack{
			playing: true,
			onSetVolume: func(float32) error {
				return errIPC
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
			Gain:           0.64,
			DesiredGain:    0.64,
		}

		status, err := pd.SetGain(context.Background(), 0.8)

		Expect(err).To(MatchError(errIPC))
		Expect(pd.DesiredGain).To(Equal(float32(0.8)))
		Expect(pd.Gain).To(Equal(float32(0.64)))
		Expect(status.Gain).To(Equal(float32(0.64)))
	})

	It("loadTrackLocked confirms initial gain before first Unpause and cleans up pending track on volume failure", func() {
		errVol := errors.New("mpv ipc: initial volume setting failed")
		closed := false
		mockNewTrack := &mockTrack{
			onSetVolume: func(float32) error {
				return errVol
			},
			onClose: func() {
				closed = true
			},
		}
		previousNewTrack := newTrack
		newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
			return mockNewTrack, nil
		}
		DeferCleanup(func() { newTrack = previousNewTrack })

		ctx := context.Background()
		repo := tests.CreateMockMediaFileRepo()
		repo.SetData(model.MediaFiles{{ID: "t1", Path: "/song.mp3"}})
		server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
		pd := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
		pd.DesiredGain = 0.5
		pd.PlaybackQueue.Add(model.MediaFiles{{ID: "t1", Path: "/song.mp3"}})

		status, err := pd.Start(ctx)

		Expect(err).To(MatchError(errVol))
		Expect(closed).To(BeTrue())
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(status.Playing).To(BeFalse())
	})

	It("Stop synchronously propagates Pause error and retains recoverable paused-track semantics without destroying track", func() {
		errPause := errors.New("mpv ipc: pause command timed out")
		track := &mockTrack{
			playing: true,
			onPause: func() error {
				return errPause
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Stop(context.Background())

		Expect(err).To(MatchError(errPause))
		Expect(pd.ActiveTrack).To(BeIdenticalTo(track))
		Expect(track.closeCalls).To(Equal(0))
		Expect(status.Playing).To(BeTrue())
	})

	It("Start synchronously propagates Unpause error without producing false Playing state", func() {
		errUnpause := errors.New("mpv ipc: unpause rejected")
		track := &mockTrack{
			playing: false,
			onUnpause: func() error {
				return errUnpause
			},
		}
		pd := &playbackDevice{
			serviceCtx:     context.Background(),
			PlaybackQueue:  NewQueue(),
			PlaybackEvents: make(chan model.PlaybackEvent, 16),
			ActiveTrack:    track,
		}

		status, err := pd.Start(context.Background())

		Expect(err).To(MatchError(errUnpause))
		Expect(status.Playing).To(BeFalse())
		Expect(pd.isPlaying()).To(BeFalse())
	})

	It("Deduplicates synchronous failure and subsequent asynchronous PlaybackEventFailure without double advance", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		errUnpause := errors.New("mpv ipc: unpause socket error")
		token := model.PlaybackToken(42)
		track := &mockTrack{
			playing: false,
			onUnpause: func() error {
				return errUnpause
			},
		}
		pd := &playbackDevice{
			serviceCtx:          ctx,
			PlaybackQueue:       NewQueue(),
			PlaybackEvents:      make(chan model.PlaybackEvent, 16),
			ActiveTrack:         track,
			activePlaybackToken: token,
		}

		// Synchronous failure
		status, err := pd.Start(ctx)
		Expect(err).To(MatchError(errUnpause))
		Expect(status.Playing).To(BeFalse())

		// Asynchronous event for the same token arrives later
		pos := 10
		pd.handlePlaybackEvent(model.PlaybackEvent{
			Token:    token,
			Kind:     model.PlaybackEventFailure,
			Position: &pos,
			Err:      errUnpause,
		})

		// Active track retired safely, recovery position preserved, no panic or second advance
		Expect(pd.ActiveTrack).To(BeNil())
		Expect(pd.recoveryPosition).ToNot(BeNil())
		Expect(*pd.recoveryPosition).To(Equal(10))
	})
})

func TestPlaybackDeviceMuteRemainsIndependentAndPrecedesFirstUnpause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device := NewPlaybackDevice(ctx, nil, "Speaker", "pulse/mute-test")
	track := &mockTrack{}
	device.ActiveTrack = track
	device.Gain = .64
	device.DesiredGain = .64

	status, err := device.executeCommand(ctx, PlaybackCommand{Action: CommandMute, Muted: true})
	if err != nil || !status.Muted || status.Gain != .64 || !track.muted {
		t.Fatalf("mute changed gain or was not confirmed: status=%#v track=%#v err=%v", status, track, err)
	}
	status, err = device.executeCommand(ctx, PlaybackCommand{Action: CommandGain, Gain: .2})
	if err != nil || !status.Muted || status.Gain != .2 {
		t.Fatalf("gain adjustment destroyed mute: %#v err=%v", status, err)
	}
	status, err = device.executeCommand(ctx, PlaybackCommand{Action: CommandMute, Muted: false})
	if err != nil || status.Muted || status.Gain != .2 || track.muted {
		t.Fatalf("unmute did not preserve current gain: %#v track=%#v err=%v", status, track, err)
	}

	sequence := []string{}
	initial := &mockTrack{
		onSetVolume: func(value float32) error { sequence = append(sequence, "volume"); return nil },
		onSetMute:   func(value bool) error { sequence = append(sequence, "mute"); return nil },
		onUnpause:   func() error { sequence = append(sequence, "unpause"); return nil },
	}
	previousNewTrack := newTrack
	newTrack = func(context.Context, chan<- model.PlaybackEvent, model.PlaybackToken, string, model.MediaFile) (Track, error) {
		return initial, nil
	}
	defer func() { newTrack = previousNewTrack }()
	device.ActiveTrack = nil
	device.Muted = true
	device.DesiredGain = .64
	device.PlaybackQueue.Set(model.MediaFiles{{ID: "mute-track", Path: "/mute.mp3"}})
	status, err = device.Start(ctx)
	if err != nil || !status.Playing || !status.Muted {
		t.Fatalf("initial muted track did not start with authoritative state: %#v err=%v", status, err)
	}
	if len(sequence) != 3 || sequence[0] != "volume" || sequence[1] != "mute" || sequence[2] != "unpause" {
		t.Fatalf("initial output commands were not confirmed before unpause: %v", sequence)
	}
}
