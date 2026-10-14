package subsonic

import (
	"context"
	"net/http/httptest"
	"os"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("JukeboxControl", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		ps     playback.PlaybackServer
		api    *Router
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		conf.Server.Jukebox.Enabled = true
		conf.Server.Jukebox.AdminOnly = false
		conf.Server.Jukebox.AutoDiscoverBluetooth = false
		conf.Server.Jukebox.Default = ""
		conf.Server.Jukebox.Devices = nil

		ps = playback.GetInstance(nil)
		go func() {
			_ = ps.Run(ctx)
		}()

		Eventually(func() error {
			_, err := ps.GetDeviceForUser("admin")
			return err
		}).Should(Succeed())

		api = &Router{playback: ps}
	})

	AfterEach(func() {
		cancel()
	})

	It("keeps the current duplicate entry when removing an earlier song", func() {
		device, err := ps.GetDeviceForUser("admin")
		Expect(err).ToNot(HaveOccurred())

		device.PlaybackQueue.Clear()
		device.PlaybackQueue.Add(model.MediaFiles{
			{ID: "dup", Path: "/music/a1.mp3"},
			{ID: "mid", Path: "/music/mid.mp3"},
			{ID: "dup", Path: "/music/a2.mp3"},
		})
		device.PlaybackQueue.SetIndex(2)

		req := httptest.NewRequest("GET", "/rest/jukeboxControl?action=remove&index=1", nil)
		req = req.WithContext(request.WithUser(req.Context(), model.User{ID: "u1", UserName: "admin", IsAdmin: true}))

		resp, err := api.JukeboxControl(req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp).ToNot(BeNil())
		Expect(resp.JukeboxStatus).ToNot(BeNil())
		Expect(resp.JukeboxStatus.CurrentIndex).To(Equal(int32(1)))
		Expect(resp.JukeboxStatus.Playing).To(BeFalse())
		Expect(device.PlaybackQueue.Current()).ToNot(BeNil())
		Expect(device.PlaybackQueue.Current().Path).To(Equal("/music/a2.mp3"))
	})

	It("rejects non-owner mutation after session heartbeat denial", func() {
		if os.Getenv("ND_TEST_SESSION_COMMAND_AUTHORITY") != "1" {
			Skip("set ND_TEST_SESSION_COMMAND_AUTHORITY=1 to run unresolved acceptance probe")
		}
		device, err := ps.GetDeviceForUser("admin")
		Expect(err).ToNot(HaveOccurred())
		sessionID := "validation-subsonic-command-owner"
		_, err = ps.AttachSession(ctx, playback.AttachRequest{
			SessionID: sessionID, ClientID: "owner-tab", User: "admin", DeviceName: device.DeviceName,
		})
		Expect(err).ToNot(HaveOccurred())
		_, err = ps.HeartbeatSession(ctx, sessionID, "non-owner-tab")
		Expect(err).To(MatchError(playback.ErrSessionOwnership))
		before, err := device.Status(ctx)
		Expect(err).ToNot(HaveOccurred())

		req := httptest.NewRequest("GET", "/rest/jukeboxControl?action=setGain&gain=0.25&sessionId="+sessionID+"&clientId=non-owner-tab", nil)
		req = req.WithContext(request.WithUser(req.Context(), model.User{ID: "u1", UserName: "admin", IsAdmin: true}))
		resp, commandErr := api.JukeboxControl(req)
		after, err := device.Status(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(commandErr).To(HaveOccurred(), "non-owner command accepted: response=%#v gain=%v->%v", resp, before.Gain, after.Gain)
		Expect(after.Gain).To(Equal(before.Gain))
	})

	It("returns failed response when jukebox is disabled", func() {
		conf.Server.Jukebox.Enabled = false
		req := httptest.NewRequest("GET", "/rest/jukeboxControl?action=status", nil)
		req = req.WithContext(request.WithUser(req.Context(), model.User{ID: "u1", UserName: "admin", IsAdmin: true}))

		resp, err := api.JukeboxControl(req)
		Expect(err).To(HaveOccurred())
		Expect(resp).To(BeNil())
		Expect(err.Error()).To(ContainSubstring("Jukebox is disabled"))
	})
})
