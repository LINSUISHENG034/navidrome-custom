package playback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockTrack struct {
	playing      bool
	pauseCalls   int
	closeCalls   int
	position     int
	volumeValues []float32
	onUnpause    func()
	onClose      func()
}

func (m *mockTrack) IsPlaying() bool { return m.playing }
func (m *mockTrack) SetVolume(value float32) {
	m.volumeValues = append(m.volumeValues, value)
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

func TestPlaybackDeviceInvokesStateChangeCallbackOnSkip(t *testing.T) {
	called := make(chan DeviceStatus, 1)
	pd := &playbackDevice{
		PlaybackQueue: NewQueue(),
		PlaybackDone:  make(chan bool, 1),
		ActiveTrack:   &mockTrack{},
		onStateChange: func(status DeviceStatus) { called <- status },
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

type pendingPlaybackHarness struct {
	ctx        context.Context
	device     *playbackDevice
	track      *mockTrack
	successor  *mockTrack
	entered    chan struct{}
	resolved   chan struct{}
	playedGain chan float32
	playedIDs  chan string
	ended      chan chan bool
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
		ended:      make(chan chan bool, 1),
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
	newTrack = func(_ context.Context, done chan bool, _ string, mf model.MediaFile) (Track, error) {
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
		h.ended <- done
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
	var ended chan bool
	select {
	case ended = <-h.ended:
	case <-time.After(time.Second):
		t.Fatal("initial output did not expose its natural-end event")
	}
	select {
	case ended <- true:
	case <-time.After(time.Second):
		t.Fatal("natural track end was not accepted")
	}
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
			PlaybackQueue: NewQueue(),
			PlaybackDone:  make(chan bool),
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
			PlaybackQueue: NewQueue(),
			PlaybackDone:  make(chan bool),
			ActiveTrack:   track,
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
			PlaybackQueue: NewQueue(),
			PlaybackDone:  make(chan bool),
			ActiveTrack:   track,
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
