package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
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

func TestJukeboxMigrationAndOperationRoutes(t *testing.T) {
	api := &Router{}
	r := chi.NewRouter()
	api.addJukeboxControlRoute(r)

	want := map[string]bool{
		"GET /jukebox/operation":             true,
		"POST /jukebox/migration/reserve":    true,
		"POST /jukebox/migration/release":    true,
		"POST /jukebox/migration/commit":     true,
		"GET /jukebox/migration/reservation": true,
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

type jukeboxMutationValidationCase struct {
	path string
	body map[string]any
}

func jukeboxMutationValidationCases() []jukeboxMutationValidationCase {
	return []jukeboxMutationValidationCase{
		{"/jukebox/set", map[string]any{"ids": []string{"must-not-reach-datastore"}}},
		{"/jukebox/start", map[string]any{}},
		{"/jukebox/play", map[string]any{}},
		{"/jukebox/pause", map[string]any{}},
		{"/jukebox/stop", map[string]any{}},
		{"/jukebox/skip", map[string]any{"index": 0}},
		{"/jukebox/seek", map[string]any{"position": 5}},
		{"/jukebox/volume", map[string]any{"gain": 0.125}},
		{"/jukebox/add", map[string]any{"ids": []string{"must-not-reach-datastore"}}},
		{"/jukebox/remove", map[string]any{"index": 0}},
		{"/jukebox/move", map[string]any{"from": 0, "to": 1}},
		{"/jukebox/devices/switch", map[string]any{"deviceName": "validation-authority-bound"}},
	}
}

func jukeboxValidationBody(body map[string]any, sessionID, clientID string) map[string]any {
	scoped := make(map[string]any, len(body)+3)
	for key, value := range body {
		scoped[key] = value
	}
	if clientID != "" {
		scoped["sessionId"], scoped["clientId"] = sessionID, clientID
		scoped["bindingGeneration"] = 1
	}
	return scoped
}

func startJukeboxLogicalFixture(t *testing.T, defaultName string, definitions []conf.AudioDeviceDefinition) (context.Context, playback.PlaybackServer) {
	t.Helper()
	previous := conf.Server.Jukebox
	conf.Server.Jukebox.AutoDiscoverBluetooth = false
	conf.Server.Jukebox.Default = defaultName
	conf.Server.Jukebox.Devices = definitions
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
		if err == nil && device.Name == defaultName && device.DeviceName == definitions[0][1] {
			return ctx, ps
		}
		if time.Now().After(deadline) {
			t.Fatal("logical device fixture not initialized")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Opt-in acceptance probes preserve unsupported session-command contracts as RED.
// They use only idle logical devices: no media, MPV, host discovery or physical output.
func TestJukeboxSessionCommandAuthorityValidation(t *testing.T) {
	if os.Getenv("ND_TEST_SESSION_COMMAND_AUTHORITY") != "1" {
		t.Skip("set ND_TEST_SESSION_COMMAND_AUTHORITY=1 to run unresolved acceptance probes")
	}
	ctx, ps := startJukeboxLogicalFixture(t, "Validation Default", []conf.AudioDeviceDefinition{
		{"Validation Default", "validation-authority-default"},
		{"Validation Bound", "validation-authority-bound"},
	})
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)
	api.addJukeboxDeviceRoute(router)
	serveAs := func(t *testing.T, path string, body any, userName string) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: userName, UserName: userName, IsAdmin: true,
		}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	serve := func(t *testing.T, path string, body any) *httptest.ResponseRecorder {
		return serveAs(t, path, body, "validation-admin")
	}
	attach := func(t *testing.T, sessionID, clientID, deviceName string) uint64 {
		t.Helper()
		response := serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": clientID, "deviceName": deviceName,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", response.Code, response.Body.String())
		}
		var status playback.SessionStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.BindingGeneration == 0 {
			t.Fatalf("attach generation: %#v err=%v", status, err)
		}
		t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, clientID) })
		return status.BindingGeneration
	}
	t.Run("non-owner cannot mutate after heartbeat denial", func(t *testing.T) {
		sessionID := uniqueSessionID("validation-command-owner")
		attach(t, sessionID, "owner-tab", "validation-authority-default")
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
		generation := attach(t, sessionID, "bound-owner", "validation-authority-bound")
		defaultDevice, err := ps.GetDeviceForUser("validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		before, err := defaultDevice.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response := serve(t, "/jukebox/volume", map[string]any{
			"sessionId": sessionID, "clientId": "bound-owner", "bindingGeneration": generation, "operationId": "bound-owner-volume", "gain": 0.5,
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
	t.Run("all mutating routes reject non-owner and bare bypass", func(t *testing.T) {
		sessionID := uniqueSessionID("all-command-owner")
		attach(t, sessionID, "owner", "validation-authority-default")
		before, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		cases := jukeboxMutationValidationCases()
		for _, test := range cases {
			for _, clientID := range []string{"non-owner", ""} {
				t.Run(test.path+"/"+clientID, func(t *testing.T) {
					body := jukeboxValidationBody(test.body, sessionID, clientID)
					response := serve(t, test.path, body)
					if response.Code != http.StatusForbidden {
						t.Fatalf("unauthorized route=%s scope=%s returned %d: %s", test.path, clientID, response.Code, response.Body.String())
					}
					after, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("denied mutation changed authority: before=%#v after=%#v err=%v", before, after, err)
					}
				})
			}
		}
	})
	t.Run("attach cannot silently steal or bypass device ownership", func(t *testing.T) {
		sessionID := uniqueSessionID("attach-authority")
		attach(t, sessionID, "owner", "validation-authority-default")
		for _, attempt := range []struct {
			sessionID string
			clientID  string
			user      string
		}{
			{sessionID, "other-tab", "validation-admin"},
			{uniqueSessionID("alternate"), "other-tab", "validation-admin"},
			{sessionID, "owner", "other-user"},
		} {
			response := serveAs(t, "/jukebox/session/attach", map[string]any{
				"sessionId": attempt.sessionID, "clientId": attempt.clientID, "deviceName": "validation-authority-default",
			}, attempt.user)
			if response.Code != http.StatusForbidden {
				t.Fatalf("attach stole authority: %+v status=%d body=%s", attempt, response.Code, response.Body.String())
			}
		}
		response := serveAs(t, "/jukebox/volume", map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": 1, "gain": 0.25, "user": "validation-admin",
		}, "other-user")
		if response.Code != http.StatusForbidden {
			t.Fatalf("forged user command accepted: %d %s", response.Code, response.Body.String())
		}
		for _, path := range []string{"/jukebox/session/heartbeat", "/jukebox/session/detach"} {
			response = serveAs(t, path, map[string]any{"sessionId": sessionID, "clientId": "owner"}, "other-user")
			if response.Code != http.StatusForbidden {
				t.Fatalf("foreign user %s accepted: %d %s", path, response.Code, response.Body.String())
			}
		}
		state, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil || state.SessionStatus.OwnerClientID != "owner" {
			t.Fatalf("owner changed after denied attach/lifecycle: %#v err=%v", state, err)
		}
	})
	t.Run("explicit same-user takeover preserves state and revokes every old-owner route", func(t *testing.T) {
		sessionID := uniqueSessionID("explicit-takeover")
		generation := attach(t, sessionID, "old-owner", "validation-authority-default")
		response := serve(t, "/jukebox/volume", map[string]any{"sessionId": sessionID, "clientId": "old-owner", "bindingGeneration": generation, "operationId": "old-owner-volume", "gain": 0.625})
		if response.Code != http.StatusOK {
			t.Fatalf("seed owner state: %d %s", response.Code, response.Body.String())
		}
		before, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		response = serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": "new-owner", "deviceName": "validation-authority-default", "takeover": true,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("explicit takeover: %d %s", response.Code, response.Body.String())
		}
		t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, "new-owner") })
		after, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil || after.SessionStatus.OwnerClientID != "new-owner" || after.SessionStatus.QueueVersion != before.SessionStatus.QueueVersion || !reflect.DeepEqual(before.JukeboxPlaylist, after.JukeboxPlaylist) {
			t.Fatalf("explicit takeover changed playback: before=%#v after=%#v err=%v", before, after, err)
		}
		for _, test := range jukeboxMutationValidationCases() {
			response = serve(t, test.path, jukeboxValidationBody(test.body, sessionID, "old-owner"))
			if response.Code != http.StatusForbidden {
				t.Fatalf("old owner retained route %s: %d %s", test.path, response.Code, response.Body.String())
			}
		}
		response = serveAs(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": "foreign", "deviceName": "validation-authority-default", "takeover": true,
		}, "other-user")
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-user explicit takeover accepted: %d %s", response.Code, response.Body.String())
		}
		response = serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": uniqueSessionID("alternate-takeover"), "clientId": "alternate", "deviceName": "validation-authority-default", "takeover": true,
		})
		if response.Code != http.StatusForbidden {
			t.Fatalf("alternate session takeover stole device: %d %s", response.Code, response.Body.String())
		}
		response = serve(t, "/jukebox/volume", map[string]any{"sessionId": sessionID, "clientId": "new-owner", "bindingGeneration": after.SessionStatus.BindingGeneration, "operationId": "new-owner-volume", "gain": 0.5})
		if response.Code != http.StatusOK {
			t.Fatalf("new owner cannot mutate: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("scope validation and unclaimed legacy access", func(t *testing.T) {
		response := serve(t, "/jukebox/volume", map[string]any{"gain": 0.4})
		if response.Code != http.StatusOK {
			t.Fatalf("unclaimed legacy gain: %d %s", response.Code, response.Body.String())
		}
		sessionID := uniqueSessionID("scope-validation")
		generation := attach(t, sessionID, "owner", "validation-authority-default")
		for _, test := range []struct {
			path string
			body map[string]any
			code int
		}{
			{"/jukebox/volume", map[string]any{"sessionId": sessionID, "gain": 0.2}, http.StatusBadRequest},
			{"/jukebox/volume", map[string]any{"sessionId": "missing", "clientId": "owner", "gain": 0.2}, http.StatusNotFound},
			{"/jukebox/volume?sessionId=conflict&clientId=owner", map[string]any{"sessionId": sessionID, "clientId": "owner", "gain": 0.2}, http.StatusBadRequest},
			{"/jukebox/volume?sessionId=" + sessionID + "&clientId=owner&bindingGeneration=" + strconv.FormatUint(generation, 10) + "&operationId=scope-volume", map[string]any{"gain": 0.3}, http.StatusOK},
		} {
			response := serve(t, test.path, test.body)
			if response.Code != test.code {
				t.Fatalf("scope %s body=%v status=%d want=%d response=%s", test.path, test.body, response.Code, test.code, response.Body.String())
			}
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

type jukeboxExecutionErrorServer struct {
	playback.PlaybackServer
	err     error
	command playback.PlaybackCommand
	calls   int
}

func (s *jukeboxExecutionErrorServer) ExecuteCommand(_ context.Context, _ playback.CommandScope, command playback.PlaybackCommand) (playback.DeviceStatus, error) {
	s.calls++
	s.command = command
	return playback.DeviceStatus{}, s.err
}

func TestJukeboxExecutionErrorsPropagate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		action  string
		body    map[string]any
		failure string
	}{
		{name: "source stop", path: "/jukebox/pause", action: playback.CommandPause, body: map[string]any{}, failure: "pause IPC rejected"},
		{name: "target start", path: "/jukebox/start", action: playback.CommandStart, body: map[string]any{}, failure: "unpause IPC rejected"},
		{name: "target volume", path: "/jukebox/volume", action: playback.CommandGain, body: map[string]any{"gain": 0.25}, failure: "volume IPC rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executionErr := errors.New(tc.failure)
			server := &jukeboxExecutionErrorServer{err: executionErr}
			api := &Router{playback: server}
			router := chi.NewRouter()
			api.addJukeboxControlRoute(router)

			encoded, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(encoded))
			r = r.WithContext(request.WithUser(r.Context(), model.User{ID: "admin", UserName: "admin", IsAdmin: true}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, r)

			if response.Code != http.StatusInternalServerError || response.Body.String() != tc.failure+"\n" {
				t.Fatalf("execution error did not propagate unchanged: status=%d body=%q", response.Code, response.Body.String())
			}
			if server.calls != 1 || server.command.Action != tc.action {
				t.Fatalf("unexpected command execution: calls=%d command=%#v", server.calls, server.command)
			}
		})
	}
}

func TestJukeboxGenerationAndMigrationHTTPContract(t *testing.T) {
	_, ps := startJukeboxLogicalFixture(t, "Migration Default", []conf.AudioDeviceDefinition{
		{"Migration Default", "mig-default"},
		{"Migration Target", "mig-target"},
	})
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)

	serve := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "admin", UserName: "admin", IsAdmin: true,
		}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}

	sessionID := uniqueSessionID("http-gen")
	attachRes := serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-default",
	})
	if attachRes.Code != http.StatusOK {
		t.Fatalf("attach failed: %d %s", attachRes.Code, attachRes.Body.String())
	}
	var attachPayload struct {
		BindingGeneration   uint64 `json:"bindingGeneration"`
		ControlHandoffToken string `json:"controlHandoffToken"`
	}
	if err := json.Unmarshal(attachRes.Body.Bytes(), &attachPayload); err != nil {
		t.Fatal(err)
	}
	if attachPayload.BindingGeneration != 1 || attachPayload.ControlHandoffToken == "" {
		t.Fatalf("unexpected attach payload: %#v", attachPayload)
	}

	// 1. Missing generation -> 400 with GENERATION_REQUIRED
	resMissing := serve(t, http.MethodPost, "/jukebox/volume", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "gain": 0.5,
	})
	if resMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing generation, got %d %s", resMissing.Code, resMissing.Body.String())
	}
	var errJson map[string]any
	_ = json.Unmarshal(resMissing.Body.Bytes(), &errJson)
	if errJson["code"] != "GENERATION_REQUIRED" {
		t.Fatalf("expected GENERATION_REQUIRED, got %#v", errJson)
	}

	// 2. Mismatched/older generation -> 409 with COMMAND_SUPERSEDED
	resSuperseded := serve(t, http.MethodPost, "/jukebox/volume", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": 99, "gain": 0.5,
	})
	if resSuperseded.Code != http.StatusConflict {
		t.Fatalf("expected 409 for mismatched generation, got %d %s", resSuperseded.Code, resSuperseded.Body.String())
	}
	_ = json.Unmarshal(resSuperseded.Body.Bytes(), &errJson)
	if errJson["code"] != "COMMAND_SUPERSEDED" {
		t.Fatalf("expected COMMAND_SUPERSEDED, got %#v", errJson)
	}

	// 3. Exact generation with stable operationId -> 200 OK
	resApplied := serve(t, http.MethodPost, "/jukebox/volume", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": 1, "operationId": "op-vol-1", "gain": 0.7,
	})
	if resApplied.Code != http.StatusOK {
		t.Fatalf("expected 200 for exact generation, got %d %s", resApplied.Code, resApplied.Body.String())
	}

	// 4. Query operation via GET /jukebox/operation
	qOpRes := serve(t, http.MethodGet, "/jukebox/operation?sessionId="+sessionID+"&operationId=op-vol-1", nil)
	if qOpRes.Code != http.StatusOK {
		t.Fatalf("query operation failed: %d %s", qOpRes.Code, qOpRes.Body.String())
	}
	var opRecord map[string]any
	_ = json.Unmarshal(qOpRes.Body.Bytes(), &opRecord)
	if opRecord["status"] != "APPLIED" || opRecord["operationId"] != "op-vol-1" {
		t.Fatalf("unexpected operation record: %#v", opRecord)
	}

	// 5. Reservation of occupied target without confirmation -> 409 TARGET_OCCUPIED
	targetSessionID := uniqueSessionID("target-occ")
	targetAttach := serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": targetSessionID, "clientId": "target-owner", "deviceName": "mig-target",
	})
	if targetAttach.Code != http.StatusOK {
		t.Fatalf("target attach failed: %d %s", targetAttach.Code, targetAttach.Body.String())
	}
	targetBefore, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
	if err != nil {
		t.Fatal(err)
	}

	occupiedRequest := map[string]any{
		"migrationId": "mig-http-1", "operationId": "g1:reserve-occupied", "sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target", "bindingGeneration": attachPayload.BindingGeneration,
	}
	resReserveOcc := serve(t, http.MethodPost, "/jukebox/migration/reserve", occupiedRequest)
	if resReserveOcc.Code != http.StatusConflict {
		t.Fatalf("expected 409 TARGET_OCCUPIED, got %d %s", resReserveOcc.Code, resReserveOcc.Body.String())
	}
	var occResp map[string]any
	_ = json.Unmarshal(resReserveOcc.Body.Bytes(), &occResp)
	if occResp["code"] != "TARGET_OCCUPIED" || occResp["sceneFingerprint"] == nil || occResp["queueCount"] != float64(0) {
		t.Fatalf("unexpected occupied response: %#v", occResp)
	}
	fp := occResp["sceneFingerprint"].(string)

	// A fingerprint is a concurrency token, not consent to take over the target.
	for _, tc := range []struct {
		name  string
		force any
	}{
		{name: "missing"},
		{name: "false", force: false},
	} {
		t.Run("fingerprint without takeover "+tc.name, func(t *testing.T) {
			body := map[string]any{
				"migrationId": "mig-http-1", "operationId": "g1:reserve-no-takeover-" + tc.name,
				"sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target",
				"bindingGeneration": attachPayload.BindingGeneration, "expectedFingerprint": fp,
			}
			if tc.force != nil {
				body["forceTakeover"] = tc.force
			}
			response := serve(t, http.MethodPost, "/jukebox/migration/reserve", body)
			var payload map[string]any
			_ = json.Unmarshal(response.Body.Bytes(), &payload)
			if response.Code != http.StatusConflict || payload["code"] != "TARGET_OCCUPIED" {
				t.Fatalf("fingerprint without explicit takeover: %d %s", response.Code, response.Body.String())
			}
			reservation, err := ps.QueryReservation(context.Background(), "mig-http-1")
			if err != nil || reservation.Status != "NOT_FOUND" {
				t.Fatalf("denied request created reservation: %#v, %v", reservation, err)
			}
			after, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
			if err != nil || !reflect.DeepEqual(targetBefore, after) {
				t.Fatalf("denied request changed target: before=%#v after=%#v err=%v", targetBefore, after, err)
			}
		})
	}

	// Renewing the target owner's binding changes its scene and invalidates the
	// previously issued fingerprint, without starting output.
	renew := serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": targetSessionID, "clientId": "target-owner", "deviceName": "mig-target", "takeover": true,
	})
	if renew.Code != http.StatusOK {
		t.Fatalf("renew target binding: %d %s", renew.Code, renew.Body.String())
	}
	targetBefore, err = ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lost occupied response retains structured conflict", func(t *testing.T) {
		query := serve(t, http.MethodGet, "/jukebox/operation?sessionId="+sessionID+"&operationId=g1:reserve-occupied", nil)
		var result map[string]any
		if err := json.Unmarshal(query.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if query.Code != http.StatusOK || result["status"] != "FAILED" || result["errorCode"] != "TARGET_OCCUPIED" || result["sceneFingerprint"] != occResp["sceneFingerprint"] || result["queueCount"] != occResp["queueCount"] {
			t.Errorf("lost conflict details: original=%#v queried=%#v status=%d", occResp, result, query.Code)
		}
		retry := serve(t, http.MethodPost, "/jukebox/migration/reserve", occupiedRequest)
		var retryBody map[string]any
		if err := json.Unmarshal(retry.Body.Bytes(), &retryBody); err != nil {
			t.Fatal(err)
		}
		if retry.Code != http.StatusConflict || !reflect.DeepEqual(occResp, retryBody) {
			t.Errorf("retry changed original conflict: original=%#v retried=%#v status=%d", occResp, retryBody, retry.Code)
		}
		reservation, err := ps.QueryReservation(context.Background(), "mig-http-1")
		if err != nil || reservation.Status != "NOT_FOUND" {
			t.Errorf("query or retry created reservation: %#v err=%v", reservation, err)
		}
		after, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
		if err != nil || !reflect.DeepEqual(targetBefore, after) {
			t.Errorf("query or retry changed newer target: before=%#v after=%#v err=%v", targetBefore, after, err)
		}
	})
	stale := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": "mig-http-1", "operationId": "g1:reserve-stale", "sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target",
		"bindingGeneration": attachPayload.BindingGeneration, "expectedFingerprint": fp, "forceTakeover": true,
	})
	var stalePayload map[string]any
	_ = json.Unmarshal(stale.Body.Bytes(), &stalePayload)
	if stale.Code != http.StatusConflict || stalePayload["code"] != "TARGET_OCCUPIED" {
		t.Fatalf("stale confirmation must conflict: %d %s", stale.Code, stale.Body.String())
	}
	newFingerprint, ok := stalePayload["sceneFingerprint"].(string)
	if !ok || newFingerprint == "" || newFingerprint == fp {
		t.Fatalf("expected a changed target fingerprint: %s", stale.Body.String())
	}
	fp = newFingerprint

	// 6. Explicit takeover with the current fingerprint is non-destructive.
	resReserveConfirm := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": "mig-http-1", "operationId": "g1:reserve-confirm", "sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target",
		"bindingGeneration": attachPayload.BindingGeneration, "expectedFingerprint": fp, "forceTakeover": true,
	})
	if resReserveConfirm.Code != http.StatusOK {
		t.Fatalf("expected 200 after confirmation, got %d %s", resReserveConfirm.Code, resReserveConfirm.Body.String())
	}
	targetAfter, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
	if err != nil || !reflect.DeepEqual(targetBefore, targetAfter) {
		t.Fatalf("reservation changed target: before=%#v after=%#v err=%v", targetBefore, targetAfter, err)
	}

	// 7. Query reservation via GET /jukebox/migration/reservation
	resQuery := serve(t, http.MethodGet, "/jukebox/migration/reservation?migrationId=mig-http-1", nil)
	if resQuery.Code != http.StatusOK {
		t.Fatalf("query reservation failed: %d %s", resQuery.Code, resQuery.Body.String())
	}
	var resInfo map[string]any
	_ = json.Unmarshal(resQuery.Body.Bytes(), &resInfo)
	if resInfo["status"] != "ACTIVE" {
		t.Fatalf("expected ACTIVE reservation, got %#v", resInfo)
	}

	// 8. Commit migration via POST /jukebox/migration/commit
	resCommit := serve(t, http.MethodPost, "/jukebox/migration/commit", map[string]any{
		"migrationId": "mig-http-1", "operationId": "g1:commit", "sessionId": sessionID, "clientId": "tab-1", "targetDeviceName": "mig-target",
		"expectedGeneration": attachPayload.BindingGeneration, "expectedFingerprint": fp,
	})
	if resCommit.Code != http.StatusOK {
		t.Fatalf("commit failed: %d %s", resCommit.Code, resCommit.Body.String())
	}
	var commitPayload map[string]any
	_ = json.Unmarshal(resCommit.Body.Bytes(), &commitPayload)
	if commitPayload["bindingGeneration"] != float64(2) || commitPayload["deviceName"] != "mig-target" {
		t.Fatalf("unexpected commit response: %#v", commitPayload)
	}

	currentGeneration := uint64(commitPayload["bindingGeneration"].(float64))
	oldTarget, err := ps.GetDeviceForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"reserve", "release", "commit"} {
		for _, tc := range []struct {
			name       string
			generation *uint64
			code       string
		}{
			{"missing", nil, "GENERATION_REQUIRED"},
			{"zero", func() *uint64 { n := uint64(0); return &n }(), "GENERATION_REQUIRED"},
			{"old", &attachPayload.BindingGeneration, "COMMAND_SUPERSEDED"},
			{"future", func() *uint64 { n := currentGeneration + 1; return &n }(), "COMMAND_SUPERSEDED"},
			{"equal", &currentGeneration, ""},
		} {
			t.Run("migration generation "+operation+"/"+tc.name, func(t *testing.T) {
				migrationID := "generation-" + operation + "-" + tc.name
				base := map[string]any{
					"migrationId": migrationID, "sessionId": sessionID, "clientId": "tab-1",
				}
				if operation != "reserve" {
					response := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
						"migrationId": migrationID, "operationId": "g2:setup-" + migrationID,
						"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": currentGeneration, "deviceName": "mig-default",
					})
					if response.Code != http.StatusOK {
						t.Fatalf("create active reservation: %d %s", response.Code, response.Body.String())
					}
				}
				beforeReservation, err := ps.QueryReservation(context.Background(), migrationID)
				if err != nil {
					t.Fatal(err)
				}
				beforeSource, err := ps.SessionSnapshot(context.Background(), sessionID, "admin")
				if err != nil {
					t.Fatal(err)
				}
				beforeQueue, beforeDevice, err := oldTarget.Get(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				base["operationId"] = "g2:" + operation + "-" + tc.name
				if tc.generation != nil {
					base["bindingGeneration"] = *tc.generation
				}
				if operation == "reserve" {
					base["deviceName"] = "mig-default"
				} else if operation == "commit" {
					base["targetDeviceName"] = "mig-default"
				}
				response := serve(t, http.MethodPost, "/jukebox/migration/"+operation, base)
				if tc.code == "" {
					if response.Code != http.StatusOK {
						t.Fatalf("current generation rejected: %d %s", response.Code, response.Body.String())
					}
				} else {
					var payload map[string]any
					_ = json.Unmarshal(response.Body.Bytes(), &payload)
					wantStatus := http.StatusConflict
					if tc.code == "GENERATION_REQUIRED" {
						wantStatus = http.StatusBadRequest
					}
					if response.Code != wantStatus || payload["code"] != tc.code {
						t.Errorf("unexpected generation response: %d %s", response.Code, response.Body.String())
					}
					afterReservation, err := ps.QueryReservation(context.Background(), migrationID)
					if err != nil || !reflect.DeepEqual(beforeReservation, afterReservation) {
						t.Errorf("rejected request changed reservation: before=%#v after=%#v err=%v", beforeReservation, afterReservation, err)
					}
					afterSource, err := ps.SessionSnapshot(context.Background(), sessionID, "admin")
					if err != nil || !reflect.DeepEqual(beforeSource, afterSource) {
						t.Errorf("rejected request changed source: before=%#v after=%#v err=%v", beforeSource, afterSource, err)
					}
					afterQueue, afterDevice, err := oldTarget.Get(context.Background())
					if err != nil || !reflect.DeepEqual(beforeQueue, afterQueue) || !reflect.DeepEqual(beforeDevice, afterDevice) {
						t.Errorf("rejected request changed target: queue=%#v -> %#v device=%#v -> %#v err=%v", beforeQueue, afterQueue, beforeDevice, afterDevice, err)
					}
				}
				if operation != "commit" || tc.code != "" {
					if beforeReservation.Status == "ACTIVE" || (operation == "reserve" && tc.code == "") {
						response := serve(t, http.MethodPost, "/jukebox/migration/release", map[string]any{
							"migrationId": migrationID, "operationId": "g2:cleanup-" + migrationID,
							"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": currentGeneration,
						})
						if response.Code != http.StatusOK {
							t.Fatalf("release fixture reservation: %d %s", response.Code, response.Body.String())
						}
					}
				}
			})
		}
	}

	current, err := ps.SessionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"reserve", "release", "commit"} {
		for _, tc := range []struct {
			name string
			id   *string
		}{
			{"missing", nil},
			{"empty", func() *string { s := ""; return &s }()},
			{"spaces", func() *string { s := "   "; return &s }()},
			{"whitespace", func() *string { s := "\t\n"; return &s }()},
		} {
			t.Run("migration operation identity "+operation+"/"+tc.name, func(t *testing.T) {
				migrationID := "identity-" + operation + "-" + tc.name
				if operation != "reserve" {
					response := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
						"migrationId": migrationID, "operationId": "g3:setup-" + migrationID,
						"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": current.BindingGeneration, "deviceName": "mig-target",
					})
					if response.Code != http.StatusOK {
						t.Fatalf("create active reservation: %d %s", response.Code, response.Body.String())
					}
				}
				beforeReservation, err := ps.QueryReservation(context.Background(), migrationID)
				if err != nil {
					t.Fatal(err)
				}
				beforeSource, err := ps.SessionSnapshot(context.Background(), sessionID, "admin")
				if err != nil {
					t.Fatal(err)
				}
				beforeTarget, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
				if err != nil {
					t.Fatal(err)
				}
				body := map[string]any{
					"migrationId": migrationID, "sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": current.BindingGeneration,
				}
				if tc.id != nil {
					body["operationId"] = *tc.id
				}
				if operation == "reserve" {
					body["deviceName"] = "mig-target"
				} else if operation == "commit" {
					body["targetDeviceName"] = "mig-target"
				}
				response := serve(t, http.MethodPost, "/jukebox/migration/"+operation, body)
				var payload map[string]any
				_ = json.Unmarshal(response.Body.Bytes(), &payload)
				if response.Code != http.StatusBadRequest || payload["code"] != "OPERATION_ID_REQUIRED" {
					t.Errorf("unexpected identity response: %d %s", response.Code, response.Body.String())
				}
				afterReservation, err := ps.QueryReservation(context.Background(), migrationID)
				if err != nil || !reflect.DeepEqual(beforeReservation, afterReservation) {
					t.Errorf("rejected request changed reservation: before=%#v after=%#v err=%v", beforeReservation, afterReservation, err)
				}
				afterSource, err := ps.SessionSnapshot(context.Background(), sessionID, "admin")
				if err != nil || !reflect.DeepEqual(beforeSource, afterSource) {
					t.Errorf("rejected request changed source: before=%#v after=%#v err=%v", beforeSource, afterSource, err)
				}
				afterTarget, err := ps.SessionSnapshot(context.Background(), targetSessionID, "admin")
				if err != nil || !reflect.DeepEqual(beforeTarget, afterTarget) {
					t.Errorf("rejected request changed target: before=%#v after=%#v err=%v", beforeTarget, afterTarget, err)
				}
				if beforeReservation.Status == "ACTIVE" {
					response = serve(t, http.MethodPost, "/jukebox/migration/release", map[string]any{
						"migrationId": migrationID, "operationId": "g3:cleanup-" + migrationID,
						"sessionId": sessionID, "clientId": "tab-1", "bindingGeneration": current.BindingGeneration,
					})
					if response.Code != http.StatusOK {
						t.Fatalf("release fixture reservation: %d %s", response.Code, response.Body.String())
					}
				}
			})
		}
	}
}

func TestJukeboxMigrationCommitWireShapeRejection(t *testing.T) {
	_, ps := startJukeboxLogicalFixture(t, "Shape Default", []conf.AudioDeviceDefinition{
		{"Shape Default", "shape-default"},
		{"Shape Target", "shape-target"},
	})
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)

	serve := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "admin", UserName: "admin", IsAdmin: true,
		}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}
	serveRaw := func(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "admin", UserName: "admin", IsAdmin: true,
		}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}

	sessionID := uniqueSessionID("shape-src")
	attachRes := serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "deviceName": "shape-default",
	})
	if attachRes.Code != http.StatusOK {
		t.Fatalf("attach failed: %d %s", attachRes.Code, attachRes.Body.String())
	}
	var attachPayload struct {
		BindingGeneration uint64 `json:"bindingGeneration"`
	}
	if err := json.Unmarshal(attachRes.Body.Bytes(), &attachPayload); err != nil {
		t.Fatal(err)
	}
	targetOwnerID := uniqueSessionID("shape-target-owner")
	targetAttach := serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": targetOwnerID, "clientId": "shape-owner", "deviceName": "shape-target",
	})
	if targetAttach.Code != http.StatusOK {
		t.Fatalf("target attach failed: %d %s", targetAttach.Code, targetAttach.Body.String())
	}

	// A device holds at most one active migration reservation, so every
	// rejection case below shares this single reservation; rejected commits must
	// leave it ACTIVE. Only the final legacy commit consumes it.
	migrationID := "mig-shape"
	generation := attachPayload.BindingGeneration
	probe := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": migrationID, "operationId": "g1:" + migrationID + "-probe", "sessionId": sessionID, "clientId": "tab-1",
		"deviceName": "shape-target", "bindingGeneration": generation,
	})
	if probe.Code != http.StatusConflict {
		t.Fatalf("expected occupied probe, got %d %s", probe.Code, probe.Body.String())
	}
	var probePayload map[string]any
	_ = json.Unmarshal(probe.Body.Bytes(), &probePayload)
	fp, ok := probePayload["sceneFingerprint"].(string)
	if !ok || fp == "" {
		t.Fatalf("probe did not return fingerprint: %#v", probePayload)
	}
	res := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": migrationID, "operationId": "g1:" + migrationID + "-take", "sessionId": sessionID, "clientId": "tab-1",
		"deviceName": "shape-target", "bindingGeneration": generation, "expectedFingerprint": fp, "forceTakeover": true,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("reserve failed: %d %s", res.Code, res.Body.String())
	}
	fpJSON, _ := json.Marshal(fp)

	commitBase := `"migrationId":"` + migrationID + `","sessionId":"%s","clientId":"tab-1","targetDeviceName":"shape-target","bindingGeneration":%d,"expectedFingerprint":` + string(fpJSON)
	commitShape := func(t *testing.T, operationID, shape string) (int, map[string]any) {
		t.Helper()
		body := fmt.Sprintf(`{"operationId":%q,`+commitBase+`,%s}`, operationID, sessionID, generation, shape)
		res := serveRaw(t, http.MethodPost, "/jukebox/migration/commit", body)
		var payload map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &payload)
		return res.Code, payload
	}
	assertShapeRejected := func(t *testing.T, name, operationID, shape string) {
		t.Helper()
		code, payload := commitShape(t, operationID, shape)
		if code != http.StatusBadRequest || payload["code"] != "MIGRATION_PROTOCOL_CONFLICT" {
			t.Fatalf("%s not rejected: %d %s", name, code, payload)
		}
		info, qErr := ps.QueryReservation(context.Background(), migrationID)
		if qErr != nil || info.Status != "ACTIVE" {
			t.Fatalf("%s consumed reservation: %#v %v", name, info, qErr)
		}
	}

	t.Run("explicit mixed legacy and identity keys rejected with state intact", func(t *testing.T) {
		beforeTarget, err := ps.SessionSnapshot(context.Background(), targetOwnerID, "admin")
		if err != nil {
			t.Fatal(err)
		}
		assertShapeRejected(t, "mixed wire keys", "g1:commit-mixed",
			`"queue":[],"currentIndex":0,"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1"`)
		afterTarget, err := ps.SessionSnapshot(context.Background(), targetOwnerID, "admin")
		if err != nil || !reflect.DeepEqual(beforeTarget, afterTarget) {
			t.Fatalf("rejected commit changed target: before=%#v after=%#v err=%v", beforeTarget, afterTarget, err)
		}
	})

	t.Run("explicit null shape key rejected", func(t *testing.T) {
		assertShapeRejected(t, "null shape key", "g1:commit-null", `"queueItems":null`)
	})

	t.Run("mixed case keys still rejected", func(t *testing.T) {
		assertShapeRejected(t, "mixed case keys", "g1:commit-case", `"Queue":[],"QueueItems":[]`)
	})

	t.Run("duplicate keys keep last and stay rejected", func(t *testing.T) {
		assertShapeRejected(t, "duplicate keys", "g1:commit-dup",
			`"queue":[],"queueItems":[],"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}]`)
	})

	t.Run("wrong type shape key stays invalid body", func(t *testing.T) {
		code, _ := commitShape(t, "g1:commit-type", `"queueItems":"nope"`)
		if code != http.StatusBadRequest {
			t.Fatalf("wrong type not rejected as invalid body: %d", code)
		}
		info, qErr := ps.QueryReservation(context.Background(), migrationID)
		if qErr != nil || info.Status != "ACTIVE" {
			t.Fatalf("invalid body consumed reservation: %#v %v", info, qErr)
		}
	})

	t.Run("legacy only explicit zero keys commit successfully then malformed replay rejected", func(t *testing.T) {
		code, commitPayload := commitShape(t, "g1:commit-legacy", `"queue":[],"currentIndex":0`)
		if code != http.StatusOK {
			t.Fatalf("explicit zero legacy commit failed: %d %#v", code, commitPayload)
		}
		currentGeneration := uint64(commitPayload["bindingGeneration"].(float64))
		if currentGeneration <= generation {
			t.Fatalf("commit did not advance binding generation: %#v", commitPayload)
		}

		body := fmt.Sprintf(`{"operationId":"g1:commit-legacy","migrationId":%q,"sessionId":%q,"clientId":"tab-1","targetDeviceName":"shape-target","bindingGeneration":%d,"expectedFingerprint":%s,"queue":[],"currentIndex":0,"queueItems":[{"queueItemId":"qi-2","mediaFileId":"track"}]}`,
			migrationID, sessionID, currentGeneration, fpJSON)
		res := serveRaw(t, http.MethodPost, "/jukebox/migration/commit", body)
		var replayPayload map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &replayPayload)
		if res.Code != http.StatusBadRequest || replayPayload["code"] != "MIGRATION_PROTOCOL_CONFLICT" {
			t.Fatalf("malformed replay not rejected as protocol conflict: %d %s", res.Code, res.Body.String())
		}
	})
}

// jukeboxExplicitWireInstanceID is the fixed canonical UUID used as the
// explicit playbackInstanceId wire value; it never collides with
// server-minted instances.
const jukeboxExplicitWireInstanceID = "7f3a2b1c-9d4e-4a5f-b6c7-d8e9f0a1b2c3"

// jukeboxExplicitInstanceFixture wires the logical-device playback server and
// an attached source/target-owner session pair for the explicit
// playbackInstanceId HTTP contract tests.
type jukeboxExplicitInstanceFixture struct {
	ps            playback.PlaybackServer
	router        *chi.Mux
	sessionID     string
	generation    uint64
	targetOwnerID string
}

func newJukeboxExplicitInstanceFixture(t *testing.T) *jukeboxExplicitInstanceFixture {
	t.Helper()
	_, ps := startJukeboxLogicalFixture(t, "Explicit Default", []conf.AudioDeviceDefinition{
		{"Explicit Default", "explicit-default"},
		{"Explicit Target", "explicit-target"},
	})
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)

	sessionID := uniqueSessionID("explicit-src")
	attachRes := serveJukeboxJSON(t, router, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": sessionID, "clientId": "tab-1", "deviceName": "explicit-default",
	})
	if attachRes.Code != http.StatusOK {
		t.Fatalf("attach failed: %d %s", attachRes.Code, attachRes.Body.String())
	}
	var attachPayload struct {
		BindingGeneration uint64 `json:"bindingGeneration"`
	}
	if err := json.Unmarshal(attachRes.Body.Bytes(), &attachPayload); err != nil {
		t.Fatal(err)
	}
	targetOwnerID := uniqueSessionID("explicit-target-owner")
	targetAttach := serveJukeboxJSON(t, router, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": targetOwnerID, "clientId": "explicit-owner", "deviceName": "explicit-target",
	})
	if targetAttach.Code != http.StatusOK {
		t.Fatalf("target attach failed: %d %s", targetAttach.Code, targetAttach.Body.String())
	}
	return &jukeboxExplicitInstanceFixture{
		ps: ps, router: router, sessionID: sessionID,
		generation: attachPayload.BindingGeneration, targetOwnerID: targetOwnerID,
	}
}

func serveJukeboxJSON(t *testing.T, router *chi.Mux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(encoded))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r = r.WithContext(request.WithUser(r.Context(), model.User{
		ID: "admin", UserName: "admin", IsAdmin: true,
	}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

func (f *jukeboxExplicitInstanceFixture) serveRaw(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	r = r.WithContext(request.WithUser(r.Context(), model.User{
		ID: "admin", UserName: "admin", IsAdmin: true,
	}))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, r)
	return rec
}

// commitInstance posts a migration commit for the no-reservation migration
// mig-explicit-none. The value matrix sends no reservation: the
// explicit-instance validation runs before the reservation lookup, so a
// conforming server answers 400 MIGRATION_PROTOCOL_CONFLICT while the
// baseline ignores the unknown key and dies later at the missing reservation
// (404). That keeps every case independent — a baseline-accepted body can
// never consume a shared reservation and cascade COMMAND_SUPERSEDED into
// later cases. The nil-datastore logical fixture cannot resolve media files,
// so the matrix uses the empty-queue vehicle; core tests cover the same
// values on non-empty queues with resolvable media.
func (f *jukeboxExplicitInstanceFixture) commitInstance(t *testing.T, operationID, shape string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"operationId":%q,"migrationId":"mig-explicit-none","sessionId":%q,"clientId":"tab-1","targetDeviceName":"explicit-target","bindingGeneration":%d,%s}`,
		operationID, f.sessionID, f.generation, shape)
	res := f.serveRaw(t, http.MethodPost, "/jukebox/migration/commit", body)
	var payload map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &payload)
	return res.Code, payload
}

func (f *jukeboxExplicitInstanceFixture) assertInstanceRejected(t *testing.T, name, operationID, shape string) {
	t.Helper()
	beforeTarget, err := f.ps.SessionSnapshot(context.Background(), f.targetOwnerID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, payload := f.commitInstance(t, operationID, shape)
	if code != http.StatusBadRequest || payload["code"] != "MIGRATION_PROTOCOL_CONFLICT" {
		t.Fatalf("%s not rejected: %d %s", name, code, payload)
	}
	afterTarget, err := f.ps.SessionSnapshot(context.Background(), f.targetOwnerID, "admin")
	if err != nil || !reflect.DeepEqual(beforeTarget, afterTarget) {
		t.Fatalf("%s changed target owner scene: before=%#v after=%#v err=%v", name, beforeTarget, afterTarget, err)
	}
}

// explicitInstanceIDJSONFragment renders `"playbackInstanceId":<encoded>`.
func explicitInstanceIDJSONFragment(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return `"playbackInstanceId":` + string(encoded)
}

func TestJukeboxMigrationCommitExplicitInstanceWireContract(t *testing.T) {
	f := newJukeboxExplicitInstanceFixture(t)
	t.Run("non-canonical explicit values rejected as protocol conflict", func(t *testing.T) {
		jukeboxExplicitValueMatrix(t, f)
	})
	t.Run("non-string explicit values stay invalid body", func(t *testing.T) {
		jukeboxExplicitNonStringMatrix(t, f)
	})
	t.Run("canonical explicit value with empty queue rejected", func(t *testing.T) {
		f.assertInstanceRejected(t, "explicit with empty queue", "g1:commit-exp-emptyqueue",
			`"queueItems":[],"playbackInstanceId":"`+jukeboxExplicitWireInstanceID+`"`)
	})
	t.Run("canonical explicit value without current item rejected", func(t *testing.T) {
		f.assertInstanceRejected(t, "explicit without current item", "g1:commit-exp-nocurrent",
			`"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"playbackInstanceId":"`+jukeboxExplicitWireInstanceID+`"`)
	})
	t.Run("invalid explicit import against completed operation rejects before cached result", func(t *testing.T) {
		jukeboxExplicitCompletedOperationGuard(t, f)
	})
	t.Run("explicit id is not a credential", func(t *testing.T) {
		jukeboxExplicitAuthorityBoundaries(t, f)
	})
	t.Run("omitted explicit id keeps idle import allowed", func(t *testing.T) {
		jukeboxExplicitOmittedIdleImport(t, f)
	})
}

// jukeboxExplicitValueMatrix runs the non-canonical string value matrix.
// Every fixture value is marshaled so the wire text is always valid JSON;
// whitespace stays inside the UUID string literal.
func jukeboxExplicitValueMatrix(t *testing.T, f *jukeboxExplicitInstanceFixture) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		value any // nil encodes JSON null
	}{
		{"null", nil},
		{"empty", ""},
		{"whitespace", " \t"},
		{"leading-space", " " + jukeboxExplicitWireInstanceID},
		{"trailing-space", jukeboxExplicitWireInstanceID + " "},
		{"uppercase", "7F3A2B1C-9D4E-4A5F-B6C7-D8E9F0A1B2C3"},
		{"braced", "{" + jukeboxExplicitWireInstanceID + "}"},
		{"urn", "urn:uuid:" + jukeboxExplicitWireInstanceID},
		{"undashed-hex", "7f3a2b1c9d4e4a5fb6c7d8e9f0a1b2c3"},
		{"truncated", "7f3a2b1c-9d4e-4a5f"},
		{"non-hex", "zf3a2b1c-9d4e-4a5f-b6c7-d8e9f0a1b2c3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operationID := "g1:commit-exp-" + tc.name
			f.assertInstanceRejected(t, tc.name, operationID,
				`"queueItems":[],`+explicitInstanceIDJSONFragment(t, tc.value))
		})
	}
}

func jukeboxExplicitNonStringMatrix(t *testing.T, f *jukeboxExplicitInstanceFixture) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"number", float64(42)},
		{"bool", true},
		{"array", []any{}},
		{"object", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := f.commitInstance(t, "g1:commit-exp-type-"+tc.name,
				`"queueItems":[],`+explicitInstanceIDJSONFragment(t, tc.value))
			if code != http.StatusBadRequest {
				t.Fatalf("non-string explicit value not rejected as invalid body: %d", code)
			}
		})
	}
}

func jukeboxExplicitCompletedOperationGuard(t *testing.T, f *jukeboxExplicitInstanceFixture) {
	t.Helper()
	// The first attempt records a FAILED operation (no reservation for
	// mig-explicit-none). Its body must be shape-valid under the explicit
	// import contract — non-empty queueItems with a selected current item —
	// so it passes validation and fails at the missing reservation,
	// recording the FAILED result the replay below collides with.
	firstCode, _ := f.commitInstance(t, "g1:commit-exp-cached",
		`"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1","playbackInstanceId":"`+jukeboxExplicitWireInstanceID+`"`)
	if firstCode != http.StatusNotFound {
		t.Fatalf("setup commit: %d", firstCode)
	}
	code, payload := f.commitInstance(t, "g1:commit-exp-cached", `"queueItems":[],"playbackInstanceId":""`)
	if code != http.StatusBadRequest || payload["code"] != "MIGRATION_PROTOCOL_CONFLICT" {
		t.Fatalf("invalid explicit import replayed cached result: %d %s", code, payload)
	}
	query := serveJukeboxJSON(t, f.router, http.MethodGet, "/jukebox/operation?sessionId="+f.sessionID+"&operationId=g1:commit-exp-cached", nil)
	var result map[string]any
	_ = json.Unmarshal(query.Body.Bytes(), &result)
	if query.Code != http.StatusOK || result["status"] != "FAILED" || result["errorCode"] != "RESERVATION_NOT_FOUND" {
		t.Fatalf("invalid explicit import disturbed the completed record: %#v status=%d", result, query.Code)
	}
}

func jukeboxExplicitAuthorityBoundaries(t *testing.T, f *jukeboxExplicitInstanceFixture) {
	t.Helper()
	// Shape-valid commit with a current item: authorization runs before
	// media resolution, so the wrong owner must be refused without the
	// explicit instance acting as a credential. This control passes on the
	// baseline too; it pins the authority boundary for the new field.
	beforeTarget, err := f.ps.SessionSnapshot(context.Background(), f.targetOwnerID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	commitBase := `"migrationId":"mig-explicit-none","sessionId":%q,"clientId":"tab-1","targetDeviceName":"explicit-target","bindingGeneration":%d`
	body := fmt.Sprintf(`{"operationId":"g1:commit-exp-wrongowner",`+commitBase+`,"clientId":"non-owner","queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1","playbackInstanceId":"%s"}`,
		f.sessionID, f.generation, jukeboxExplicitWireInstanceID)
	res := f.serveRaw(t, http.MethodPost, "/jukebox/migration/commit", body)
	var payload map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &payload)
	if res.Code != http.StatusForbidden || payload["code"] != "SESSION_OWNERSHIP" {
		t.Fatalf("explicit instance bypassed ownership: %d %s", res.Code, res.Body.String())
	}
	afterTarget, err := f.ps.SessionSnapshot(context.Background(), f.targetOwnerID, "admin")
	if err != nil || !reflect.DeepEqual(beforeTarget, afterTarget) {
		t.Fatalf("denied explicit commit changed target: before=%#v after=%#v err=%v", beforeTarget, afterTarget, err)
	}

	staleBody := fmt.Sprintf(`{"operationId":"g1:commit-exp-stalegen",`+commitBase+`,"bindingGeneration":%d,"queueItems":[{"queueItemId":"qi-1","mediaFileId":"track"}],"currentQueueItemId":"qi-1","playbackInstanceId":"%s"}`,
		f.sessionID, f.generation, f.generation+1, jukeboxExplicitWireInstanceID)
	res = f.serveRaw(t, http.MethodPost, "/jukebox/migration/commit", staleBody)
	_ = json.Unmarshal(res.Body.Bytes(), &payload)
	if res.Code != http.StatusConflict || payload["code"] != "COMMAND_SUPERSEDED" {
		t.Fatalf("explicit instance bypassed stale generation: %d %s", res.Code, res.Body.String())
	}
	afterTarget, err = f.ps.SessionSnapshot(context.Background(), f.targetOwnerID, "admin")
	if err != nil || !reflect.DeepEqual(beforeTarget, afterTarget) {
		t.Fatalf("stale explicit commit changed target: before=%#v after=%#v err=%v", beforeTarget, afterTarget, err)
	}
}

func jukeboxExplicitOmittedIdleImport(t *testing.T, f *jukeboxExplicitInstanceFixture) {
	t.Helper()
	// The idle import without playbackInstanceId must stay legal on the
	// conforming server; this control also passes on the baseline. It
	// needs a real reservation, so it uses its own migrationId and takes
	// the occupied-probe fingerprint flow like the existing wire-shape
	// test.
	idleMigrationID := "mig-explicit-idle"
	probe := serveJukeboxJSON(t, f.router, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": idleMigrationID, "operationId": "g1:" + idleMigrationID + "-probe", "sessionId": f.sessionID, "clientId": "tab-1",
		"deviceName": "explicit-target", "bindingGeneration": f.generation,
	})
	if probe.Code != http.StatusConflict {
		t.Fatalf("expected occupied probe, got %d %s", probe.Code, probe.Body.String())
	}
	var probePayload map[string]any
	_ = json.Unmarshal(probe.Body.Bytes(), &probePayload)
	fp, ok := probePayload["sceneFingerprint"].(string)
	if !ok || fp == "" {
		t.Fatalf("probe did not return fingerprint: %#v", probePayload)
	}
	res := serveJukeboxJSON(t, f.router, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": idleMigrationID, "operationId": "g1:" + idleMigrationID + "-take", "sessionId": f.sessionID, "clientId": "tab-1",
		"deviceName": "explicit-target", "bindingGeneration": f.generation, "expectedFingerprint": fp, "forceTakeover": true,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("reserve failed: %d %s", res.Code, res.Body.String())
	}
	fpJSON, _ := json.Marshal(fp)
	body := fmt.Sprintf(`{"operationId":"g1:commit-exp-omitted","migrationId":%q,"sessionId":%q,"clientId":"tab-1","targetDeviceName":"explicit-target","bindingGeneration":%d,"expectedFingerprint":%s,"queueItems":[]}`,
		idleMigrationID, f.sessionID, f.generation, fpJSON)
	res = f.serveRaw(t, http.MethodPost, "/jukebox/migration/commit", body)
	var commitPayload map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &commitPayload)
	if res.Code != http.StatusOK {
		t.Fatalf("omitted explicit idle import rejected: %d %s", res.Code, res.Body.String())
	}
	if commitPayload["playbackInstanceId"] != nil && commitPayload["playbackInstanceId"] != "" {
		t.Fatalf("idle import minted an instance: %#v", commitPayload)
	}
}

func TestJukeboxProtectedAnchorMoveVersionPrecondition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		clientID   string
		protected  bool
		version    *int
		statusCode int
		code       string
	}{
		{name: "omitted", clientID: "owner", protected: true, statusCode: http.StatusBadRequest, code: "QUEUE_VERSION_REQUIRED"},
		{name: "wrong owner omitted", clientID: "observer", protected: true, statusCode: http.StatusForbidden, code: "SESSION_OWNERSHIP"},
		{name: "explicit zero", clientID: "owner", protected: true, version: new(int), statusCode: http.StatusNotFound, code: "QUEUE_ITEM_NOT_FOUND"},
		{name: "unprotected omitted", statusCode: http.StatusNotFound, code: "QUEUE_ITEM_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			namespace := t.Name()
			deviceName := namespace + "-device"
			sessionID := namespace + "-session"
			ownerID := namespace + "-owner"
			clientID := namespace + "-" + tc.clientID
			ctx, ps := startJukeboxLogicalFixture(t, deviceName, []conf.AudioDeviceDefinition{{deviceName, deviceName}})
			ctx = request.WithUser(ctx, model.User{ID: "validation-admin", UserName: "validation-admin", IsAdmin: true})
			body := map[string]any{"queueItemId": "missing-source", "anchorQueueItemId": "missing-anchor", "placement": "before"}
			if tc.protected {
				attached, err := ps.AttachSession(ctx, playback.AttachRequest{SessionID: sessionID, User: "validation-admin", ClientID: ownerID, DeviceName: deviceName})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, ownerID) })
				body["sessionId"], body["clientId"] = sessionID, clientID
				body["bindingGeneration"] = attached.BindingGeneration
				body["operationId"] = fmt.Sprintf("g%d:%s", attached.BindingGeneration, namespace)
			}
			if tc.version != nil {
				body["expectedQueueVersion"] = *tc.version
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			api := &Router{playback: ps}
			router := chi.NewRouter()
			api.addJukeboxControlRoute(router)
			r := httptest.NewRequest(http.MethodPost, "/jukebox/move", bytes.NewReader(encoded)).WithContext(ctx)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, r)
			var payload struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("non-JSON response: %d %s: %v", response.Code, response.Body.String(), err)
			}
			if response.Code != tc.statusCode || payload.Code != tc.code {
				t.Fatalf("version admission: got %d %s, want %d %s", response.Code, response.Body.String(), tc.statusCode, tc.code)
			}
		})
	}
}

func TestJukeboxAnchorMoveProtocolValidation(t *testing.T) {
	ctx, ps := startJukeboxLogicalFixture(t, "Anchor Default", []conf.AudioDeviceDefinition{
		{"Anchor Default", "anchor-default"},
	})
	_ = ctx
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
	attach := func(t *testing.T, sessionID, clientID string) uint64 {
		t.Helper()
		response := serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": clientID, "deviceName": "anchor-default",
		})
		if response.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", response.Code, response.Body.String())
		}
		var status playback.SessionStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, clientID) })
		return status.BindingGeneration
	}

	scope := map[string]any{
		"sessionId": "anchor-session", "clientId": "anchor-tab",
		"bindingGeneration": 1, "operationId": "g1:anchor-move-validation",
	}

	// T13-AC05: relative-anchor fields combined with any legacy from/to/toIndex
	// field are rejected with 400 MOVE_PROTOCOL_CONFLICT.
	combos := []map[string]any{
		{"queueItemId": "q-1", "anchorQueueItemId": "q-2", "placement": "before", "from": 0},
		{"queueItemId": "q-1", "anchorQueueItemId": "q-2", "placement": "before", "to": 1},
		{"queueItemId": "q-1", "anchorQueueItemId": "q-2", "placement": "before", "index": 1},
	}
	for _, combo := range combos {
		body := make(map[string]any, len(scope)+len(combo))
		for k, v := range scope {
			body[k] = v
		}
		for k, v := range combo {
			body[k] = v
		}
		response := serve(t, "/jukebox/move", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("anchor+legacy move accepted: %d %s", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("conflict payload: %v", err)
		}
		if payload["code"] != "MOVE_PROTOCOL_CONFLICT" {
			t.Fatalf("conflict code: %v (%s)", payload["code"], response.Body.String())
		}
	}

	// T13-AC10: protected incremental edits lacking instance identity are
	// explicitly rejected; unprotected legacy compat stays reachable.
	generation := attach(t, "anchor-session", "anchor-tab")
	_ = generation
	protected := make(map[string]any, len(scope)+2)
	for k, v := range scope {
		protected[k] = v
	}
	protected["from"], protected["to"] = 0, 1
	response := serve(t, "/jukebox/move", protected)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("protected legacy move accepted: %d %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["code"] != "MOVE_PROTOCOL_CONFLICT" {
		t.Fatalf("protected legacy move code: %v err=%v (%s)", payload, err, response.Body.String())
	}

	// T13-AC04: a stale expected queue version fails with 409
	// QUEUE_VERSION_CONFLICT and a fresh snapshot rather than queueing a drag.
	stale := make(map[string]any, len(scope)+4)
	for k, v := range scope {
		stale[k] = v
	}
	stale["queueItemId"] = "q-1"
	stale["anchorQueueItemId"] = "q-2"
	stale["placement"] = "before"
	stale["expectedQueueVersion"] = 7
	response = serve(t, "/jukebox/move", stale)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale version not conflicted: %d %s", response.Code, response.Body.String())
	}
	payload = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("conflict payload: %v", err)
	}
	if payload["code"] != "QUEUE_VERSION_CONFLICT" {
		t.Fatalf("version conflict code: %v (%s)", payload["code"], response.Body.String())
	}
	if _, ok := payload["status"].(map[string]any); !ok {
		t.Fatalf("version conflict lacks fresh snapshot: %s", response.Body.String())
	}

	// T13-AC04: missing source returns its distinct error with a fresh snapshot.
	missingSource := make(map[string]any, len(scope)+4)
	for k, v := range scope {
		missingSource[k] = v
	}
	missingSource["queueItemId"] = "q-missing"
	missingSource["anchorQueueItemId"] = "q-2"
	missingSource["placement"] = "before"
	missingSource["expectedQueueVersion"] = 0
	missingSource["operationId"] = "g1:anchor-move-missing-source"
	response = serve(t, "/jukebox/move", missingSource)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing source not found: %d %s", response.Code, response.Body.String())
	}
	payload = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("missing source payload: %v", err)
	}
	if payload["code"] != "QUEUE_ITEM_NOT_FOUND" {
		t.Fatalf("missing source code: %v (%s)", payload["code"], response.Body.String())
	}
	if payload["queueItemId"] != "q-missing" {
		t.Fatalf("missing source identity: %v", payload)
	}
}

type jukeboxStatusSnapshotServer struct {
	playback.PlaybackServer
	snapshot  playback.SessionSnapshot
	sessionID string
	user      string
}

func (s *jukeboxStatusSnapshotServer) SessionSnapshot(_ context.Context, sessionID, user string) (playback.SessionSnapshot, error) {
	s.sessionID, s.user = sessionID, user
	return s.snapshot, nil
}

func TestJukeboxModeHTTPContract(t *testing.T) {
	ctx, server := startJukeboxLogicalFixture(t, "Mode Default", []conf.AudioDeviceDefinition{{"Mode Default", "mode-default"}})
	attached, err := server.AttachSession(ctx, playback.AttachRequest{
		SessionID: "mode-api", User: "validation-admin", ClientID: "owner", DeviceName: "mode-default",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = server.DetachSession(context.Background(), "mode-api", "owner") })
	device, err := server.GetDeviceForUser("validation-admin")
	if err != nil {
		t.Fatal(err)
	}
	api := &Router{playback: server}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	serve := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		r = r.WithContext(request.WithUser(r.Context(), model.User{ID: "validation-admin", UserName: "validation-admin", IsAdmin: true}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	for index, tc := range []struct {
		name  string
		mode  any
		omit  bool
		valid bool
	}{
		{"order", "order", false, true},
		{"order loop", "orderLoop", false, true},
		{"single loop", "singleLoop", false, true},
		{"shuffle", "shufflePlay", false, true},
		{"same shuffle", "shufflePlay", false, true},
		{"empty", "", false, false},
		{"unknown", "repeat", false, false},
		{"case", "ORDER", false, false},
		{"whitespace", " order", false, false},
		{"null", nil, false, false},
		{"omitted", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := device.Status(ctx)
			body := map[string]any{
				"sessionId": attached.SessionID, "clientId": "owner", "bindingGeneration": attached.BindingGeneration,
				"operationId": fmt.Sprintf("g%d:mode-%d", attached.BindingGeneration, index),
			}
			if !tc.omit {
				body["mode"] = tc.mode
			}
			response := serve(t, http.MethodPost, "/jukebox/mode", body)
			wantCode := http.StatusBadRequest
			if tc.valid {
				wantCode = http.StatusOK
			}
			if response.Code != wantCode {
				t.Fatalf("mode status=%d want=%d body=%s", response.Code, wantCode, response.Body.String())
			}
			after, _ := device.Status(ctx)
			if !tc.valid {
				var payload struct{ Code string }
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != "INVALID_PLAY_MODE" || after != before {
					t.Fatalf("invalid mode mutated state or lost error code: before=%#v after=%#v response=%s err=%v", before, after, response.Body.String(), err)
				}
				return
			}
			want := before
			want.Mode = playback.PlaybackMode(tc.mode.(string))
			if want.Mode != before.Mode {
				want.StateSequence++
			}
			if after != want {
				t.Errorf("mode write lost state or ordering: got=%#v want=%#v", after, want)
			}
			for _, path := range []string{"/jukebox/status", "/jukebox/status?sessionId=" + attached.SessionID} {
				statusResponse := serve(t, http.MethodGet, path, nil)
				var payload jukeboxStatusResponse
				if err := json.Unmarshal(statusResponse.Body.Bytes(), &payload); err != nil || statusResponse.Code != http.StatusOK || payload.PlayMode != after.Mode || payload.StateSequence != after.StateSequence {
					t.Errorf("mode status mapping %s: %s err=%v", path, statusResponse.Body.String(), err)
				}
			}
		})
	}
}

func TestJukeboxManualNavigationHTTPContract(t *testing.T) {
	ctx, ps := startJukeboxLogicalFixture(t, "Navigation Default", []conf.AudioDeviceDefinition{
		{"Navigation Default", "navigation-default"},
	})
	api := &Router{playback: ps}
	router := chi.NewRouter()
	api.addJukeboxControlRoute(router)
	api.addJukeboxSessionRoute(router)

	serve := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "validation-admin", UserName: "validation-admin", IsAdmin: true,
		}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}
	attach := func(t *testing.T, sessionID, clientID string, takeover bool) uint64 {
		t.Helper()
		body := map[string]any{
			"sessionId": sessionID, "clientId": clientID, "deviceName": "navigation-default",
		}
		if takeover {
			body["takeover"] = true
		}
		response := serve(t, http.MethodPost, "/jukebox/session/attach", body)
		if response.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", response.Code, response.Body.String())
		}
		var status playback.SessionStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, clientID) })
		return status.BindingGeneration
	}
	snapshot := func(t *testing.T, sessionID string) playback.SessionSnapshot {
		t.Helper()
		state, err := ps.SessionSnapshot(ctx, sessionID, "validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	assertNoMutation := func(t *testing.T, sessionID string, before playback.SessionSnapshot) {
		t.Helper()
		if after := snapshot(t, sessionID); !reflect.DeepEqual(before, after) {
			t.Errorf("rejected navigation mutated state: before=%#v after=%#v", before, after)
		}
	}

	// Both manual navigation actions reuse the protected ExecuteCommand path:
	// the owner is admitted on an empty queue as a no-op, and the response
	// carries the authoritative ordering, mode, and instance mappings.
	for _, action := range []struct {
		path string
		op   string
	}{
		{"/jukebox/next", "next"},
		{"/jukebox/prev", "prev"},
	} {
		t.Run(action.op+"/owner admitted as empty queue no-op", func(t *testing.T) {
			sessionID := uniqueSessionID("nav-" + action.op)
			generation := attach(t, sessionID, "owner", false)
			before := snapshot(t, sessionID)
			response := serve(t, http.MethodPost, action.path, map[string]any{
				"sessionId": sessionID, "clientId": "owner", "bindingGeneration": generation,
				"operationId": fmt.Sprintf("g%d:nav-%s-owner", generation, action.op),
			})
			if response.Code != http.StatusOK {
				t.Fatalf("%s status=%d want=200 body=%s", action.path, response.Code, response.Body.String())
			}
			var payload jukeboxStatusResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("%s response is not a status mapping: %s err=%v", action.path, response.Body.String(), err)
			}
			if payload.StateSequence != before.SessionStatus.StateSequence ||
				payload.QueueVersion != before.SessionStatus.QueueVersion ||
				string(payload.PlayMode) != before.SessionStatus.PlayMode ||
				payload.PlaybackInstanceID != "" {
				t.Errorf("%s response lost ordering/mode/instance mappings: %s", action.path, response.Body.String())
			}
			assertNoMutation(t, sessionID, before)
		})
	}

	t.Run("next/query scope with empty body admitted", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-query")
		generation := attach(t, sessionID, "owner", false)
		before := snapshot(t, sessionID)
		path := fmt.Sprintf("/jukebox/next?sessionId=%s&clientId=owner&bindingGeneration=%d&operationId=g%d:nav-next-query",
			sessionID, generation, generation)
		response := serve(t, http.MethodPost, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("query scope navigation: %d %s", response.Code, response.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("prev/body and query scope conflict rejected without mutation", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-conflict")
		generation := attach(t, sessionID, "owner", false)
		before := snapshot(t, sessionID)
		response := serve(t, http.MethodPost, "/jukebox/prev?sessionId=other-session&clientId=owner", map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": generation,
			"operationId": fmt.Sprintf("g%d:nav-prev-conflict", generation),
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("scope conflict: %d %s", response.Code, response.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("next/wrong generation rejected without mutation", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-generation")
		generation := attach(t, sessionID, "owner", false)
		before := snapshot(t, sessionID)
		stale := generation + 1
		response := serve(t, http.MethodPost, "/jukebox/next", map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": stale,
			"operationId": fmt.Sprintf("g%d:nav-next-stale", stale),
		})
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusConflict || payload["code"] != "COMMAND_SUPERSEDED" {
			t.Fatalf("wrong generation: %d %s", response.Code, response.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("prev/non-owner rejected without mutation", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-nonowner")
		generation := attach(t, sessionID, "owner", false)
		before := snapshot(t, sessionID)
		response := serve(t, http.MethodPost, "/jukebox/prev", map[string]any{
			"sessionId": sessionID, "clientId": "intruder", "bindingGeneration": generation,
			"operationId": fmt.Sprintf("g%d:nav-prev-intruder", generation),
		})
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusForbidden || payload["code"] != "SESSION_OWNERSHIP" {
			t.Fatalf("non-owner navigation: %d %s", response.Code, response.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("prev/old generation after takeover rejected without mutation", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-takeover")
		oldGeneration := attach(t, sessionID, "old-owner", false)
		currentGeneration := attach(t, sessionID, "new-owner", true)
		if currentGeneration <= oldGeneration {
			t.Fatalf("takeover did not advance binding generation: old=%d current=%d", oldGeneration, currentGeneration)
		}
		before := snapshot(t, sessionID)
		response := serve(t, http.MethodPost, "/jukebox/prev", map[string]any{
			"sessionId": sessionID, "clientId": "new-owner", "bindingGeneration": oldGeneration,
			"operationId": fmt.Sprintf("g%d:nav-prev-old-generation", oldGeneration),
		})
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusConflict || payload["code"] != "COMMAND_SUPERSEDED" {
			t.Fatalf("stale generation after takeover: %d %s", response.Code, response.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("next/operation replay idempotent", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-replay")
		generation := attach(t, sessionID, "owner", false)
		body := map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": generation,
			"operationId": fmt.Sprintf("g%d:nav-next-replay", generation),
		}
		first := serve(t, http.MethodPost, "/jukebox/next", body)
		if first.Code != http.StatusOK {
			t.Fatalf("first navigation: %d %s", first.Code, first.Body.String())
		}
		var firstPayload jukeboxStatusResponse
		if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, sessionID)
		replay := serve(t, http.MethodPost, "/jukebox/next", body)
		if replay.Code != http.StatusOK {
			t.Fatalf("replayed navigation: %d %s", replay.Code, replay.Body.String())
		}
		var replayPayload jukeboxStatusResponse
		if err := json.Unmarshal(replay.Body.Bytes(), &replayPayload); err != nil {
			t.Fatal(err)
		}
		if replayPayload.StateSequence != firstPayload.StateSequence || replayPayload.QueueVersion != firstPayload.QueueVersion {
			t.Errorf("replay re-executed navigation: first=%#v replay=%#v", firstPayload, replayPayload)
		}
		query := serve(t, http.MethodGet, fmt.Sprintf("/jukebox/operation?sessionId=%s&operationId=%s",
			sessionID, body["operationId"]), nil)
		var record map[string]any
		_ = json.Unmarshal(query.Body.Bytes(), &record)
		if query.Code != http.StatusOK || record["status"] != "APPLIED" {
			t.Errorf("replay operation record: %d %s", query.Code, query.Body.String())
		}
		assertNoMutation(t, sessionID, before)
	})

	t.Run("prev/non-default mode retained in navigation response", func(t *testing.T) {
		sessionID := uniqueSessionID("nav-mode")
		generation := attach(t, sessionID, "owner", false)
		modeResponse := serve(t, http.MethodPost, "/jukebox/mode", map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": generation,
			"operationId": fmt.Sprintf("g%d:nav-mode-seed", generation), "mode": "shufflePlay",
		})
		if modeResponse.Code != http.StatusOK {
			t.Fatalf("seed mode: %d %s", modeResponse.Code, modeResponse.Body.String())
		}
		seeded := snapshot(t, sessionID)
		if seeded.SessionStatus.PlayMode != string(playback.PlaybackModeShuffle) {
			t.Fatalf("mode seed did not apply: %#v", seeded.SessionStatus)
		}
		response := serve(t, http.MethodPost, "/jukebox/prev", map[string]any{
			"sessionId": sessionID, "clientId": "owner", "bindingGeneration": generation,
			"operationId": fmt.Sprintf("g%d:nav-prev-mode", generation),
		})
		if response.Code != http.StatusOK {
			t.Fatalf("mode navigation: %d %s", response.Code, response.Body.String())
		}
		var payload jukeboxStatusResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if string(payload.PlayMode) != seeded.SessionStatus.PlayMode ||
			payload.StateSequence != seeded.SessionStatus.StateSequence ||
			payload.QueueVersion != seeded.SessionStatus.QueueVersion {
			t.Errorf("navigation response lost mode or ordering: %s", response.Body.String())
		}
		if after := snapshot(t, sessionID); !reflect.DeepEqual(seeded, after) {
			t.Errorf("empty queue navigation mutated mode state: before=%#v after=%#v", seeded, after)
		}
	})
}

func TestJukeboxStateSequenceStatusMappings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sequence uint64
		mode     playback.PlaybackMode
	}{
		{"explicit zero", 0, playback.PlaybackModeOrder},
		{"semantic sequence distinct from queue version", 17, playback.PlaybackModeShuffle},
		{"single loop", 13, playback.PlaybackModeRepeatTrack},
		{"order loop", 5, playback.PlaybackModeRepeatList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &jukeboxStatusSnapshotServer{snapshot: playback.SessionSnapshot{
				SessionStatus: playback.SessionStatus{StateSequence: tc.sequence, QueueVersion: 3, PlayMode: string(tc.mode), PlaybackInstanceID: "instance-" + string(tc.mode)},
			}}
			api := &Router{playback: server}
			router := chi.NewRouter()
			api.addJukeboxControlRoute(router)
			r := httptest.NewRequest(http.MethodGet, "/jukebox/status?sessionId=sequence-session", nil)
			r = r.WithContext(request.WithUser(r.Context(), model.User{UserName: "admin"}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, r)
			if response.Code != http.StatusOK || server.sessionID != "sequence-session" || server.user != "admin" {
				t.Fatalf("status did not read bound snapshot: status=%d session=%q user=%q", response.Code, server.sessionID, server.user)
			}
			encoded, err := json.Marshal(toStatusResponse(playback.DeviceStatus{StateSequence: tc.sequence, QueueVersion: 3, Mode: tc.mode, PlaybackInstanceID: "instance-" + string(tc.mode)}))
			if err != nil {
				t.Fatal(err)
			}
			for _, mapping := range []struct {
				name string
				body []byte
			}{
				{"device response", encoded},
				{"session status response", response.Body.Bytes()},
			} {
				var payload struct {
					StateSequence      *uint64 `json:"stateSequence"`
					QueueVersion       int     `json:"queueVersion"`
					PlayMode           string  `json:"playMode"`
					PlaybackInstanceID string  `json:"playbackInstanceId"`
				}
				if err := json.Unmarshal(mapping.body, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.StateSequence == nil || *payload.StateSequence != tc.sequence || payload.QueueVersion != 3 || payload.PlayMode != string(tc.mode) || payload.PlaybackInstanceID != "instance-"+string(tc.mode) {
					t.Errorf("%s lost sequence, structural version, or mode: %s", mapping.name, mapping.body)
				}
			}
		})
	}
}

func TestJukeboxSessionStatusIncludesQueueVersion(t *testing.T) {
	ctx, ps := startJukeboxLogicalFixture(t, "Status Default", []conf.AudioDeviceDefinition{
		{"Status Default", "status-default"},
	})
	_ = ctx
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
	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "validation-admin", UserName: "validation-admin", IsAdmin: true,
		}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}

	device, err := ps.GetDeviceForUser("validation-admin")
	if err != nil {
		t.Fatal(err)
	}
	device.PlaybackQueue.Add(model.MediaFiles{{ID: "seed", Path: "/seed.mp3"}})
	// Bump the authoritative queue version via an unprotected incremental edit.
	if response := serve(t, "/jukebox/remove", map[string]any{"index": 0}); response.Code != http.StatusOK {
		t.Fatalf("seed remove: %d %s", response.Code, response.Body.String())
	}
	if response := serve(t, "/jukebox/session/attach", map[string]any{
		"sessionId": "status-session", "clientId": "status-tab", "deviceName": "status-default",
	}); response.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", response.Code, response.Body.String())
	}
	t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), "status-session", "status-tab") })

	response := get(t, "/jukebox/status?sessionId=status-session")
	if response.Code != http.StatusOK {
		t.Fatalf("session status: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		QueueVersion int `json:"queueVersion"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.QueueVersion != 1 {
		t.Fatalf("session status dropped queue version: got=%d want=1 (%s)", payload.QueueVersion, response.Body.String())
	}
}

func TestJukeboxQueueProtocolCapabilityAdvertised(t *testing.T) {
	ctx, ps := startJukeboxLogicalFixture(t, "Capability Default", []conf.AudioDeviceDefinition{
		{"Capability Default", "capability-default"},
	})
	_ = ctx
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

	response := serve(t, "/jukebox/session/attach", map[string]any{
		"sessionId": "cap-session", "clientId": "cap-tab", "deviceName": "capability-default",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", response.Code, response.Body.String())
	}
	t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), "cap-session", "cap-tab") })
	var attached struct {
		QueueProtocol *struct {
			Version                  int  `json:"version"`
			RelativeMove             bool `json:"relativeMove"`
			IdentityRemove           bool `json:"identityRemove"`
			IdentityMigration        bool `json:"identityMigration"`
			AnchorVersionEnforcement bool `json:"anchorVersionEnforcement"`
		} `json:"queueProtocol"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &attached); err != nil {
		t.Fatal(err)
	}
	if attached.QueueProtocol == nil || attached.QueueProtocol.Version != 1 ||
		!attached.QueueProtocol.RelativeMove || !attached.QueueProtocol.IdentityRemove ||
		!attached.QueueProtocol.IdentityMigration || !attached.QueueProtocol.AnchorVersionEnforcement {
		t.Fatalf("attach did not advertise queue protocol: %s", response.Body.String())
	}

	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(request.WithUser(r.Context(), model.User{
			ID: "validation-admin", UserName: "validation-admin", IsAdmin: true,
		}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	response = get(t, "/jukebox/status?sessionId=cap-session")
	if response.Code != http.StatusOK {
		t.Fatalf("session status: %d %s", response.Code, response.Body.String())
	}
	var statusPayload struct {
		QueueProtocol *struct {
			Version int `json:"version"`
		} `json:"queueProtocol"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload.QueueProtocol == nil || statusPayload.QueueProtocol.Version != 1 {
		t.Fatalf("session status dropped queue protocol: %s", response.Body.String())
	}
}
