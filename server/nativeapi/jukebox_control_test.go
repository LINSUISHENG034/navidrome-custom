package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
)

func TestJukeboxControlAliases(t *testing.T) {
	api := &Router{}
	r := chi.NewRouter()
	api.addJukeboxControlRoute(r)

	want := map[string]bool{
		"POST /jukebox/play":  true,
		"POST /jukebox/pause": true,
		"POST /jukebox/seek":  true,
	}

	err := chi.Walk(r, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = false
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for route, missing := range want {
		if missing {
			t.Fatalf("missing expected route: %s", route)
		}
	}
}

func TestJukeboxIncrementalRoutes(t *testing.T) {
	api := &Router{}
	r := chi.NewRouter()
	api.addJukeboxControlRoute(r)

	want := map[string]bool{
		"POST /jukebox/add":    true,
		"POST /jukebox/remove": true,
		"POST /jukebox/move":   true,
	}

	err := chi.Walk(r, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = false
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for route, missing := range want {
		if missing {
			t.Fatalf("missing expected route: %s", route)
		}
	}
}

// Opt-in acceptance probes preserve unsupported session-command contracts as RED.
// They use only idle logical devices: no media, MPV, host discovery or physical output.
func TestJukeboxSessionCommandAuthorityValidation(t *testing.T) {
	if os.Getenv("ND_TEST_SESSION_COMMAND_AUTHORITY") != "1" {
		t.Skip("set ND_TEST_SESSION_COMMAND_AUTHORITY=1 to run unresolved acceptance probes")
	}
	previous := conf.Server.Jukebox
	conf.Server.Jukebox.AutoDiscoverBluetooth = false
	conf.Server.Jukebox.Default = "Validation Default"
	conf.Server.Jukebox.Devices = []conf.AudioDeviceDefinition{
		{"Validation Default", "validation-authority-default"},
		{"Validation Bound", "validation-authority-bound"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps := playback.GetInstance(nil)
	finished := make(chan error, 1)
	go func() { finished <- ps.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("playback fixture shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("playback fixture did not finish")
		}
		conf.Server.Jukebox = previous
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		device, err := ps.GetDeviceForUser("validation-admin")
		if err == nil && device.DeviceName == "validation-authority-default" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("logical device fixture not initialized")
		}
		time.Sleep(10 * time.Millisecond)
	}
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)
	serve := func(t *testing.T, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "validation-admin", UserName: "validation-admin", IsAdmin: true,
		}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	t.Run("non-owner cannot mutate after heartbeat denial", func(t *testing.T) {
		sessionID := uniqueSessionID("validation-command-owner")
		attached := serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": "owner-tab",
			"deviceName": "validation-authority-default",
		})
		if attached.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", attached.Code, attached.Body.String())
		}
		denied := serve(t, "/jukebox/session/heartbeat", map[string]any{
			"sessionId": sessionID, "clientId": "non-owner-tab",
		})
		if denied.Code != http.StatusForbidden {
			t.Fatalf("non-owner heartbeat: %d %s", denied.Code, denied.Body.String())
		}
		before, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		response := serve(t, "/jukebox/volume", map[string]any{
			"sessionId": sessionID, "clientId": "non-owner-tab", "gain": 0.25,
		})
		after, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusForbidden || after.SessionStatus.Gain != before.SessionStatus.Gain {
			t.Fatalf("non-owner mutation accepted: heartbeat=%d command=%d gain=%v->%v owner=%s",
				denied.Code, response.Code, before.SessionStatus.Gain, after.SessionStatus.Gain,
				after.SessionStatus.OwnerClientID)
		}
	})
	t.Run("owner command targets session-bound non-default device", func(t *testing.T) {
		sessionID := uniqueSessionID("validation-command-bound")
		attached := serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": "bound-owner",
			"deviceName": "validation-authority-bound",
		})
		if attached.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", attached.Code, attached.Body.String())
		}
		defaultDevice, err := ps.GetDeviceForUser("validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		before, err := defaultDevice.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response := serve(t, "/jukebox/volume", map[string]any{
			"sessionId": sessionID, "clientId": "bound-owner", "gain": 0.5,
		})
		bound, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		after, err := defaultDevice.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || bound.SessionStatus.Gain != 0.5 || after.Gain != before.Gain {
			t.Fatalf("session-bound command reached wrong device: HTTP=%d bound=%s gain=%v default gain=%v->%v",
				response.Code, bound.SessionStatus.DeviceName, bound.SessionStatus.Gain, before.Gain, after.Gain)
		}
	})
}

func TestJukeboxSessionPlaybackAPIsCompile(t *testing.T) {
	var api Router
	if api.playback == nil {
		return
	}

	_, _ = api.playback.AttachSession(context.Background(), playback.AttachRequest{SessionID: "s1", ClientID: "tab-1"})
	_, _ = api.playback.HeartbeatSession(context.Background(), "s1", "tab-1")
	_, _ = api.playback.DetachSession(context.Background(), "s1", "tab-1")
	_, _ = api.playback.SessionStatus(context.Background(), "s1")
}
