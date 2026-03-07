package subsonic

import (
	"context"
	"net/http/httptest"

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
