package subsonic

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func subsonicMutationValidationActions() []string {
	return []string{
		"set&id=must-not-reach-datastore", "start", "stop", "skip&index=0", "add&id=must-not-reach-datastore",
		"clear", "remove&index=0", "shuffle", "setGain&gain=0.25",
	}
}

var _ = Describe("JukeboxControl", func() {
	var (
		ctx           context.Context
		cancel        context.CancelFunc
		ps            playback.PlaybackServer
		api           *Router
		finished      chan error
		restoreConfig func()
	)

	BeforeEach(func() {
		previous := conf.Server.Jukebox
		restoreConfig = func() { conf.Server.Jukebox = previous }
		ctx, cancel = context.WithCancel(context.Background())
		conf.Server.Jukebox.Enabled = true
		conf.Server.Jukebox.AdminOnly = false
		conf.Server.Jukebox.AutoDiscoverBluetooth = false
		conf.Server.Jukebox.Default = ""
		conf.Server.Jukebox.Devices = nil

		ps = playback.GetInstance(nil)
		finished = make(chan error, 1)
		go func() { finished <- ps.Run(ctx) }()

		Eventually(func() error {
			_, err := ps.GetDeviceForUser("admin")
			return err
		}).Should(Succeed())

		api = &Router{playback: ps}
	})

	AfterEach(func() {
		cancel()
		Eventually(finished).Should(Receive(BeNil()))
		restoreConfig()
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
		DeferCleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, "owner-tab") })
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

	It("enforces session authority for every mutation and bare legacy bypass", func() {
		device, err := ps.GetDeviceForUser("admin")
		Expect(err).ToNot(HaveOccurred())
		sessionID := "validation-all-subsonic-commands"
		attachment, err := ps.AttachSession(ctx, playback.AttachRequest{
			SessionID: sessionID, ClientID: "owner", User: "admin", DeviceName: device.DeviceName,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, "owner") })
		before, err := device.Status(ctx)
		Expect(err).ToNot(HaveOccurred())
		serve := func(action, scope, userName string) (*responses.Subsonic, error) {
			req := httptest.NewRequest("GET", "/rest/jukeboxControl?action="+action+scope, nil)
			req = req.WithContext(request.WithUser(req.Context(), model.User{ID: userName, UserName: userName, IsAdmin: true}))
			return api.JukeboxControl(req)
		}
		assertDenied := func(action, scope, userName string) {
			response, err := serve(action, scope, userName)
			Expect(err).To(HaveOccurred(), "action=%s scope=%s returned=%#v", action, scope, response)
			Expect(response).To(BeNil())
			after, err := device.Status(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(after).To(Equal(before), "action=%s scope=%s changed state", action, scope)
		}
		for _, action := range subsonicMutationValidationActions() {
			for _, scope := range []string{"", "&sessionId=" + sessionID + "&clientId=other"} {
				assertDenied(action, scope, "admin")
			}
		}
		ownerScope := "&sessionId=" + sessionID + "&clientId=owner&bindingGeneration=" + strconv.FormatUint(attachment.BindingGeneration, 10) + "&operationId=g1:subsonic-set-gain"
		assertDenied("setGain&gain=0.25", ownerScope, "other-user")
		response, err := serve("setGain&gain=0.25", ownerScope, "admin")
		Expect(err).ToNot(HaveOccurred())
		Expect(response.JukeboxStatus.Gain).To(Equal(float32(0.25)))
		attachment, err = ps.AttachSession(ctx, playback.AttachRequest{
			SessionID: sessionID, ClientID: "new-owner", User: "admin", DeviceName: device.DeviceName, Takeover: true,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, "new-owner") })
		before, err = device.Status(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(before.Gain).To(Equal(float32(0.25)))
		for _, action := range subsonicMutationValidationActions() {
			assertDenied(action, ownerScope, "admin")
		}
		newOwnerScope := "&sessionId=" + sessionID + "&clientId=new-owner&bindingGeneration=" + strconv.FormatUint(attachment.BindingGeneration, 10) + "&operationId=g2:subsonic-set-gain"
		response, err = serve("setGain&gain=0.5", newOwnerScope, "admin")
		Expect(err).ToNot(HaveOccurred())
		Expect(response.JukeboxStatus.Gain).To(Equal(float32(0.5)))
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
