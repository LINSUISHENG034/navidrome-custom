package playback

import (
	"context"
	"time"

	"github.com/navidrome/navidrome/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PlaybackServer", func() {
	var ps *playbackServer
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		ps = &playbackServer{
			ctx: &ctx,
			playbackDevices: []playbackDevice{
				*NewPlaybackDevice(ctx, nil, "Speaker", "alsa_output.analog"),
				*NewPlaybackDevice(ctx, nil, "BT Headphones", "pulse/bluez_output.AA_BB_CC.a2dp-sink"),
			},
		}
		ps.playbackDevices[0].Default = true
	})

	Describe("ListDevices", func() {
		It("returns all devices with correct metadata", func() {
			devices := ps.ListDevices()
			Expect(devices).To(HaveLen(2))

			Expect(devices[0].Name).To(Equal("Speaker"))
			Expect(devices[0].IsDefault).To(BeTrue())
			Expect(devices[0].IsBluetooth).To(BeFalse())
			Expect(devices[0].Connected).To(BeTrue())

			Expect(devices[1].Name).To(Equal("BT Headphones"))
			Expect(devices[1].IsDefault).To(BeFalse())
			Expect(devices[1].IsBluetooth).To(BeTrue())
		})
	})

	Describe("hasDevice", func() {
		It("finds existing devices", func() {
			Expect(ps.hasDevice("alsa_output.analog")).To(BeTrue())
			Expect(ps.hasDevice("pulse/bluez_output.AA_BB_CC.a2dp-sink")).To(BeTrue())
		})

		It("returns false for unknown devices", func() {
			Expect(ps.hasDevice("nonexistent")).To(BeFalse())
		})
	})

	Describe("SwitchDevice", func() {
		It("switches the default device", func() {
			err := ps.SwitchDevice(ctx, "pulse/bluez_output.AA_BB_CC.a2dp-sink")
			Expect(err).ToNot(HaveOccurred())

			Expect(ps.playbackDevices[0].Default).To(BeFalse())
			Expect(ps.playbackDevices[1].Default).To(BeTrue())
		})

		It("returns error for unknown device", func() {
			err := ps.SwitchDevice(ctx, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("device not found"))
		})

		It("migrates queue to new device when switching", func() {
			mfs := model.MediaFiles{
				{ID: "1", Path: "/music/song1.mp3"},
				{ID: "2", Path: "/music/song2.mp3"},
			}
			ps.playbackDevices[0].PlaybackQueue.Add(mfs)
			ps.playbackDevices[0].PlaybackQueue.SetIndex(1)
			ps.playbackDevices[0].Gain = 0.75

			err := ps.SwitchDevice(ctx, "pulse/bluez_output.AA_BB_CC.a2dp-sink")
			Expect(err).ToNot(HaveOccurred())

			newDev := &ps.playbackDevices[1]
			Expect(newDev.PlaybackQueue.Size()).To(Equal(2))
			Expect(newDev.PlaybackQueue.Index).To(Equal(1))
			Expect(newDev.Gain).To(Equal(float32(0.75)))
		})

		It("does not migrate queue when switching to same device", func() {
			mfs := model.MediaFiles{
				{ID: "1", Path: "/music/song1.mp3"},
			}
			ps.playbackDevices[0].PlaybackQueue.Add(mfs)

			err := ps.SwitchDevice(ctx, "alsa_output.analog")
			Expect(err).ToNot(HaveOccurred())

			// Queue should remain on the original device, not duplicated
			Expect(ps.playbackDevices[0].Default).To(BeTrue())
			Expect(ps.playbackDevices[1].PlaybackQueue.Size()).To(Equal(0))
		})
	})

	Describe("getDefaultDevice", func() {
		It("returns the default device", func() {
			dev, err := ps.getDefaultDevice()
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Name).To(Equal("Speaker"))
		})

		It("returns error when no default is set", func() {
			ps.playbackDevices[0].Default = false
			_, err := ps.getDefaultDevice()
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("playbackDeviceContext", func() {
		It("prefers server context over canceled request context", func() {
			serverCtx := context.Background()
			ps.ctx = &serverCtx

			reqCtx, cancel := context.WithCancel(context.Background())
			cancel()

			got := ps.playbackDeviceContext(reqCtx)
			Expect(got.Err()).To(BeNil())
		})

		It("falls back to request context when server context is not set", func() {
			ps.ctx = nil
			reqCtx, cancel := context.WithCancel(context.Background())
			cancel()

			got := ps.playbackDeviceContext(reqCtx)
			Expect(got.Err()).ToNot(BeNil())
		})
	})

	It("invokes state change callback after automatic track advance", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		events := make(chan DeviceStatus, 1)
		ps = &playbackServer{
			ctx: &ctx,
			onDeviceStateChange: func(_ *playbackDevice, status DeviceStatus) {
				events <- status
			},
		}

		oldTrack := &mockTrack{playing: true}
		nextTrack := &mockTrack{}
		previousNewTrack := newTrack
		newTrack = func(_ context.Context, _ chan bool, _ string, _ model.MediaFile) (Track, error) {
			return nextTrack, nil
		}
		defer func() { newTrack = previousNewTrack }()

		pd := ps.newPlaybackDevice(ctx, "Speaker", "auto")
		pd.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}, {ID: "2", Path: "/b.mp3"}})
		pd.ActiveTrack = oldTrack

		go pd.trackSwitcherGoroutine()
		pd.PlaybackDone <- true

		Eventually(events, time.Second).Should(Receive(Equal(DeviceStatus{
			CurrentIndex: 1,
			Playing:      true,
			Gain:         DefaultGain,
			Position:     0,
		})))
	})
})
