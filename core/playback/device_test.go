package playback

import (
	"context"
	"sync"

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
