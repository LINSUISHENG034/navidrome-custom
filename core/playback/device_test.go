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
}
func (m *mockTrack) Position() int { return m.position }
func (m *mockTrack) SetPosition(offset int) error {
	m.position = offset
	return nil
}
func (m *mockTrack) Close() {
	m.closeCalls++
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

func testPlaybackDeviceCancelsPendingStart(t *testing.T, stop func(*playbackDevice, context.Context) (DeviceStatus, error), resume bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}})
	server := &playbackServer{ctx: &ctx, datastore: &tests.MockDataStore{MockedMediaFile: repo}}
	device := NewPlaybackDevice(ctx, server, "Speaker", "pulse/test")
	if _, err := device.Set(ctx, []string{"1"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	track := &mockTrack{}
	successor := &mockTrack{}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var releaseOnce sync.Once
	var startStatus DeviceStatus
	var startErr error
	previousNewTrack := newTrack
	calls := 0
	newTrack = func(context.Context, chan bool, string, model.MediaFile) (Track, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			return track, nil
		}
		return successor, nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("pending start did not finish during cleanup")
		}
		newTrack = previousNewTrack
	})
	go func() {
		startStatus, startErr = device.Start(ctx)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("start did not begin preparing the output")
	}

	stopped, err := stop(device, ctx)
	if err != nil || stopped.Playing {
		t.Fatalf("stop while preparing: status=%#v, err=%v", stopped, err)
	}
	if resume {
		resumed, err := device.Start(ctx)
		if err != nil || !resumed.Playing {
			t.Fatalf("successor start: status=%#v, err=%v", resumed, err)
		}
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("start did not finish after releasing the output")
	}
	if startErr != nil {
		t.Fatalf("start completion: %v", startErr)
	}
	status, err := device.Status(ctx)
	if err != nil || status.Playing != resume || startStatus.Playing != resume {
		t.Fatalf("cancelled preparation changed playback: status=%#v, start=%#v, err=%v", status, startStatus, err)
	}
	if track.playing || track.closeCalls != 1 {
		t.Fatalf("cancelled output was not retired: playing=%v, closes=%d", track.playing, track.closeCalls)
	}
	if resume && (!successor.playing || successor.closeCalls != 0) {
		t.Fatalf("cancelled output displaced successor: playing=%v, closes=%d", successor.playing, successor.closeCalls)
	}
	queue, _, err := device.Get(ctx)
	if err != nil || len(queue) != 1 || queue[0].ID != "1" {
		t.Fatalf("cancellation changed the authoritative queue: queue=%#v, err=%v", queue, err)
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
