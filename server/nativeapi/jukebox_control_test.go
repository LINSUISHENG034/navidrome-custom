package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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
	attach := func(t *testing.T, sessionID, clientID, deviceName string) {
		t.Helper()
		response := serve(t, "/jukebox/session/attach", map[string]any{
			"sessionId": sessionID, "clientId": clientID, "deviceName": deviceName,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("attach: %d %s", response.Code, response.Body.String())
		}
		t.Cleanup(func() { _, _ = ps.DetachSession(context.Background(), sessionID, clientID) })
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
		attach(t, sessionID, "bound-owner", "validation-authority-bound")
		defaultDevice, err := ps.GetDeviceForUser("validation-admin")
		if err != nil {
			t.Fatal(err)
		}
		before, err := defaultDevice.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response := serve(t, "/jukebox/volume", map[string]any{
			"sessionId": sessionID, "clientId": "bound-owner", "bindingGeneration": 1, "gain": 0.5,
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
		attach(t, sessionID, "old-owner", "validation-authority-default")
		response := serve(t, "/jukebox/volume", map[string]any{"sessionId": sessionID, "clientId": "old-owner", "bindingGeneration": 1, "gain": 0.625})
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
		response = serve(t, "/jukebox/volume", map[string]any{"sessionId": sessionID, "clientId": "new-owner", "bindingGeneration": 2, "gain": 0.5})
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
		attach(t, sessionID, "owner", "validation-authority-default")
		for _, test := range []struct {
			path string
			body map[string]any
			code int
		}{
			{"/jukebox/volume", map[string]any{"sessionId": sessionID, "gain": 0.2}, http.StatusBadRequest},
			{"/jukebox/volume", map[string]any{"sessionId": "missing", "clientId": "owner", "gain": 0.2}, http.StatusNotFound},
			{"/jukebox/volume?sessionId=conflict&clientId=owner", map[string]any{"sessionId": sessionID, "clientId": "owner", "gain": 0.2}, http.StatusBadRequest},
			{"/jukebox/volume?sessionId=" + sessionID + "&clientId=owner&bindingGeneration=1", map[string]any{"gain": 0.3}, http.StatusOK},
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
	serve(t, http.MethodPost, "/jukebox/session/attach", map[string]any{
		"sessionId": targetSessionID, "clientId": "target-owner", "deviceName": "mig-target",
	})
	serve(t, http.MethodPost, "/jukebox/volume", map[string]any{
		"sessionId": targetSessionID, "clientId": "target-owner", "bindingGeneration": 1, "gain": 0.3,
	})

	resReserveOcc := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": "mig-http-1", "sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target",
	})
	if resReserveOcc.Code != http.StatusConflict {
		t.Fatalf("expected 409 TARGET_OCCUPIED, got %d %s", resReserveOcc.Code, resReserveOcc.Body.String())
	}
	var occResp map[string]any
	_ = json.Unmarshal(resReserveOcc.Body.Bytes(), &occResp)
	if occResp["code"] != "TARGET_OCCUPIED" || occResp["sceneFingerprint"] == nil {
		t.Fatalf("unexpected occupied response: %#v", occResp)
	}
	fp := occResp["sceneFingerprint"].(string)

	// 6. Reservation with expected fingerprint -> 200 APPLIED
	resReserveConfirm := serve(t, http.MethodPost, "/jukebox/migration/reserve", map[string]any{
		"migrationId": "mig-http-1", "sessionId": sessionID, "clientId": "tab-1", "deviceName": "mig-target",
		"expectedFingerprint": fp,
	})
	if resReserveConfirm.Code != http.StatusOK {
		t.Fatalf("expected 200 after confirmation, got %d %s", resReserveConfirm.Code, resReserveConfirm.Body.String())
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
		"migrationId": "mig-http-1", "sessionId": sessionID, "clientId": "tab-1", "targetDeviceName": "mig-target",
		"expectedGeneration": 1, "expectedFingerprint": fp,
	})
	if resCommit.Code != http.StatusOK {
		t.Fatalf("commit failed: %d %s", resCommit.Code, resCommit.Body.String())
	}
	var commitPayload map[string]any
	_ = json.Unmarshal(resCommit.Body.Bytes(), &commitPayload)
	if commitPayload["bindingGeneration"] != float64(2) || commitPayload["deviceName"] != "mig-target" {
		t.Fatalf("unexpected commit response: %#v", commitPayload)
	}
}
