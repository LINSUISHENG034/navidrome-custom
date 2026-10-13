package playback

import (
	"context"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback/bluetooth"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPlaybackServerGetReturnsIndependentQueueSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := tests.CreateMockMediaFileRepo()
	repo.SetData(model.MediaFiles{
		{ID: "track-a", Path: "/a.mp3", Title: "A"},
		{ID: "track-b", Path: "/b.mp3", Title: "B"},
		{ID: "track-c", Path: "/c.mp3", Title: "C"},
	})
	server := &playbackServer{
		ctx:       &ctx,
		datastore: &tests.MockDataStore{MockedMediaFile: repo},
	}
	device := NewPlaybackDevice(ctx, server, "Speaker", "speaker")
	device.Default = true
	server.playbackDevices = []*playbackDevice{device}

	device, err := server.GetDeviceForUser("admin")
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if _, err := device.Set(ctx, []string{"track-a", "track-b", "track-c"}); err != nil {
		t.Fatalf("set queue: %v", err)
	}

	previousQueue, previousStatus, err := device.Get(ctx)
	if err != nil {
		t.Fatalf("get initial queue: %v", err)
	}
	if len(previousQueue) != 3 || previousQueue[0].ID != "track-a" || previousQueue[1].ID != "track-b" || previousQueue[2].ID != "track-c" {
		t.Fatalf("unexpected initial queue: %#v", previousQueue)
	}
	if previousStatus.CurrentIndex != 0 {
		t.Fatalf("unexpected initial status: %#v", previousStatus)
	}

	if _, err := device.Remove(ctx, 0); err != nil {
		t.Fatalf("remove first track: %v", err)
	}
	if len(previousQueue) != 3 || previousQueue[0].ID != "track-a" || previousQueue[1].ID != "track-b" || previousQueue[2].ID != "track-c" {
		t.Errorf("previous queue snapshot changed after Remove: %#v", previousQueue)
	}
	if previousStatus.CurrentIndex != 0 {
		t.Errorf("previous status snapshot changed after Remove: %#v", previousStatus)
	}

	currentQueue, currentStatus, err := device.Get(ctx)
	if err != nil {
		t.Fatalf("get queue after Remove: %v", err)
	}
	if len(currentQueue) != 2 || currentQueue[0].ID != "track-b" || currentQueue[1].ID != "track-c" {
		t.Fatalf("unexpected queue after Remove: %#v", currentQueue)
	}
	if currentStatus.CurrentIndex != -1 {
		t.Fatalf("unexpected status after Remove: %#v", currentStatus)
	}

	currentQueue[0].ID = "controller-mutated"
	latestQueue, latestStatus, err := device.Get(ctx)
	if err != nil {
		t.Fatalf("get queue after caller mutation: %v", err)
	}
	if len(latestQueue) != 2 || latestQueue[0].ID != "track-b" || latestQueue[1].ID != "track-c" {
		t.Errorf("caller mutation changed current queue: %#v", latestQueue)
	}
	if latestStatus.CurrentIndex != -1 {
		t.Errorf("unexpected status after caller mutation: %#v", latestStatus)
	}
}

func TestPlaybackServerDiscoveryPreservesNaturalAdvanceAuthority(t *testing.T) {
	for _, discovery := range []struct {
		name string
		run  func(*playbackServer, context.Context) error
	}{
		{name: "RefreshDevices", run: (*playbackServer).RefreshDevices},
		{name: "ListDevices", run: func(server *playbackServer, _ context.Context) error {
			server.ListDevices()
			return nil
		}},
	} {
		t.Run(discovery.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			repo := tests.CreateMockMediaFileRepo()
			repo.SetData(model.MediaFiles{{ID: "1", Path: "/a.mp3"}, {ID: "2", Path: "/b.mp3"}})
			server := &playbackServer{
				ctx:            &ctx,
				datastore:      &tests.MockDataStore{MockedMediaFile: repo},
				sessionManager: NewSessionManager(time.Minute),
			}
			initial := NewPlaybackDevice(ctx, server, "BT speaker", "pulse/bluez_output.AA_BB_CC.a2dp-sink")
			initial.Default = true
			server.playbackDevices = []*playbackDevice{initial}

			previousDiscover := discoverAllSinks
			previousAutoDiscover := conf.Server.Jukebox.AutoDiscoverBluetooth
			conf.Server.Jukebox.AutoDiscoverBluetooth = true
			discoverAllSinks = func(context.Context) []bluetooth.AudioSink {
				return []bluetooth.AudioSink{
					{Name: "bluez_output.AA_BB_CC.a2dp-sink"},
					{Name: "bluez_output.DD_EE_FF.a2dp-sink"},
				}
			}
			previousNewTrack := newTrack
			ended := make(chan model.PlaybackEvent, 1)
			newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, token model.PlaybackToken, _ string, mf model.MediaFile) (Track, error) {
				if mf.ID == "1" {
					ended <- model.PlaybackEvent{Token: token, Kind: model.PlaybackEventNaturalEnd}
					return &mockTrack{position: 7}, nil
				}
				return &mockTrack{}, nil
			}
			t.Cleanup(func() {
				cancel()
				newTrack = previousNewTrack
				discoverAllSinks = previousDiscover
				conf.Server.Jukebox.AutoDiscoverBluetooth = previousAutoDiscover
			})

			playingDevice, err := server.GetDeviceForUser("admin")
			if err != nil {
				t.Fatalf("get playing device: %v", err)
			}
			if _, err := playingDevice.Set(ctx, []string{"1", "2"}); err != nil {
				t.Fatalf("set queue: %v", err)
			}
			if status, err := playingDevice.Start(ctx); err != nil || !status.Playing {
				t.Fatalf("start: status=%#v, err=%v", status, err)
			}
			if _, err := server.AttachSession(ctx, AttachRequest{SessionID: "discovery-session", ClientID: "tab-1", User: "admin"}); err != nil {
				t.Fatalf("attach session: %v", err)
			}
			if err := discovery.run(server, ctx); err != nil {
				t.Fatalf("discover device: %v", err)
			}
			if devices := server.ListDevices(); len(devices) != 2 || !devices[1].IsBluetooth || !devices[1].Connected {
				t.Fatalf("new bluetooth device was not discovered: %#v", devices)
			}

			outputEnded := <-ended
			select {
			case playingDevice.PlaybackEvents <- outputEnded:
			case <-time.After(time.Second):
				t.Fatal("natural track end was not accepted")
			}
			deadline := time.After(time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			var live DeviceStatus
			for {
				live, err = playingDevice.Status(ctx)
				if err != nil {
					t.Fatalf("live device status: %v", err)
				}
				if live.CurrentIndex == 1 && live.Playing && live.Position == 0 {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline:
					t.Fatalf("live playback did not advance to the second track: %#v", live)
				}
			}

			registeredDevice, err := server.GetDeviceForUser("admin")
			if err != nil {
				t.Fatalf("get registered device: %v", err)
			}
			queue, registered, err := registeredDevice.Get(ctx)
			if err != nil || len(queue) != 2 || queue[1].ID != "2" || registered != live {
				t.Errorf("registered device disagrees with live second-track playback: live=%#v, registered=%#v, queueLength=%d, err=%v", live, registered, len(queue), err)
			}
			session, err := server.SessionStatus(ctx, "discovery-session")
			if err != nil || session.TrackID != "2" || session.CurrentIndex != live.CurrentIndex || session.Playing != live.Playing || session.Position != live.Position {
				t.Errorf("session disagrees with live second-track playback: live=%#v, session=%#v, err=%v", live, session, err)
			}
		})
	}
}

var _ = Describe("PlaybackServer", func() {
	var ps *playbackServer
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		ps = &playbackServer{
			ctx: &ctx,
			playbackDevices: []*playbackDevice{
				NewPlaybackDevice(ctx, nil, "Speaker", "alsa_output.analog"),
				NewPlaybackDevice(ctx, nil, "BT Headphones", "pulse/bluez_output.AA_BB_CC.a2dp-sink"),
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

		It("reuses one sink snapshot for bluetooth merge and status", func() {
			previousAutoDiscover := conf.Server.Jukebox.AutoDiscoverBluetooth
			previousDiscover := discoverAllSinks
			defer func() {
				conf.Server.Jukebox.AutoDiscoverBluetooth = previousAutoDiscover
				discoverAllSinks = previousDiscover
			}()

			conf.Server.Jukebox.AutoDiscoverBluetooth = true
			ps.playbackDevices = []*playbackDevice{
				NewPlaybackDevice(ctx, nil, "Speaker", "alsa_output.analog"),
			}
			ps.playbackDevices[0].Default = true

			calls := 0
			discoverAllSinks = func(context.Context) []bluetooth.AudioSink {
				calls++
				return []bluetooth.AudioSink{
					{Name: "bluez_output.24_C4_06_FA_00_37.a2dp-sink"},
				}
			}

			devices := ps.ListDevices()

			Expect(calls).To(Equal(1))
			Expect(devices).To(HaveLen(2))
			Expect(devices[1].Name).To(Equal("Bluetooth 24:C4:06:FA:00:37"))
			Expect(devices[1].DeviceName).To(Equal("pulse/bluez_output.24_C4_06_FA_00_37.a2dp-sink"))
			Expect(devices[1].IsBluetooth).To(BeTrue())
			Expect(devices[1].Connected).To(BeTrue())
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

			newDev := ps.playbackDevices[1]
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

		It("rebinds attached sessions to the migrated device", func() {
			ps.sessionManager = NewSessionManager(time.Minute)
			mfs := model.MediaFiles{
				{ID: "1", Path: "/music/song1.mp3"},
				{ID: "2", Path: "/music/song2.mp3"},
			}
			ps.playbackDevices[0].PlaybackQueue.Add(mfs)
			ps.playbackDevices[0].PlaybackQueue.SetIndex(1)
			ps.playbackDevices[0].queueVersion = 4

			_, err := ps.AttachSession(ctx, AttachRequest{
				SessionID:  "session-1",
				User:       "admin",
				ClientID:   "tab-1",
				DeviceName: "alsa_output.analog",
			})
			Expect(err).ToNot(HaveOccurred())

			err = ps.SwitchDevice(ctx, "pulse/bluez_output.AA_BB_CC.a2dp-sink")
			Expect(err).ToNot(HaveOccurred())

			status, err := ps.SessionStatus(ctx, "session-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(status.DeviceName).To(Equal("pulse/bluez_output.AA_BB_CC.a2dp-sink"))
			Expect(status.CurrentIndex).To(Equal(1))
			Expect(status.TrackID).To(Equal("2"))
		})

		It("clears stale authority from the old device after migration", func() {
			mfs := model.MediaFiles{
				{ID: "1", Path: "/music/song1.mp3"},
				{ID: "2", Path: "/music/song2.mp3"},
			}
			ps.playbackDevices[0].PlaybackQueue.Add(mfs)
			ps.playbackDevices[0].PlaybackQueue.SetIndex(1)

			err := ps.SwitchDevice(ctx, "pulse/bluez_output.AA_BB_CC.a2dp-sink")
			Expect(err).ToNot(HaveOccurred())

			Expect(ps.playbackDevices[0].PlaybackQueue.Size()).To(Equal(0))
			Expect(ps.playbackDevices[0].PlaybackQueue.Index).To(Equal(-1))
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
		newTrack = func(_ context.Context, _ chan<- model.PlaybackEvent, _ model.PlaybackToken, _ string, _ model.MediaFile) (Track, error) {
			return nextTrack, nil
		}
		defer func() { newTrack = previousNewTrack }()

		pd := ps.newPlaybackDevice(ctx, "Speaker", "auto")
		pd.PlaybackQueue.Add(model.MediaFiles{{ID: "1", Path: "/a.mp3"}, {ID: "2", Path: "/b.mp3"}})
		pd.mu.Lock()
		pd.ActiveTrack = oldTrack
		pd.activePlaybackToken = 1
		pd.nextPlaybackToken = 1
		pd.mu.Unlock()

		go pd.trackSwitcherGoroutine()
		pd.PlaybackEvents <- model.PlaybackEvent{Token: 1, Kind: model.PlaybackEventNaturalEnd}

		Eventually(events, time.Second).Should(Receive(Equal(DeviceStatus{
			CurrentIndex: 1,
			Playing:      true,
			Gain:         DefaultGain,
			Position:     0,
		})))
	})
})
