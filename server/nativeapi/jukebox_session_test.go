package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/playback"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
)

func TestJukeboxSessionRoutes(t *testing.T) {
	api := &Router{}
	r := chi.NewRouter()
	api.addJukeboxControlRoute(r)
	api.addJukeboxSessionRoute(r)

	want := map[string]bool{
		"GET /jukebox/session/status":     true,
		"POST /jukebox/session/attach":    true,
		"POST /jukebox/session/heartbeat": true,
		"POST /jukebox/session/detach":    true,
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

func TestJukeboxSessionHandlers(t *testing.T) {
	api := &Router{playback: playback.GetInstance(nil)}
	attachID := uniqueSessionID("attach")
	forbiddenHeartbeatID := uniqueSessionID("forbidden-heartbeat")
	forbiddenDetachID := uniqueSessionID("forbidden-detach")
	missingHeartbeatID := uniqueSessionID("missing-heartbeat")
	missingStatusID := uniqueSessionID("missing-status")
	detachSnapshotID := uniqueSessionID("detach-snapshot")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		setup      func(t *testing.T)
		handler    func(*Router, http.ResponseWriter, *http.Request)
		wantStatus int
		assertJSON func(t *testing.T, body []byte)
	}{
		{
			name:   "attach returns authoritative state",
			method: http.MethodPost,
			path:   "/jukebox/session/attach",
			body: map[string]any{
				"sessionId":  attachID,
				"clientId":   "tab-1",
				"deviceName": "pulse/test",
			},
			handler:    (*Router).jukeboxSessionAttach,
			wantStatus: http.StatusOK,
			assertJSON: func(t *testing.T, body []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got["sessionId"] != attachID || got["ownerClientId"] != "tab-1" || got["deviceName"] != "pulse/test" {
					t.Fatalf("unexpected attach response: %#v", got)
				}
				if got["attached"] != true {
					t.Fatalf("expected attached=true, got %#v", got)
				}
			},
		},
		{
			name:       "attach validates required fields",
			method:     http.MethodPost,
			path:       "/jukebox/session/attach",
			body:       map[string]any{"clientId": "tab-1"},
			handler:    (*Router).jukeboxSessionAttach,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "heartbeat maps not found to 404",
			method: http.MethodPost,
			path:   "/jukebox/session/heartbeat",
			body: map[string]any{
				"sessionId": missingHeartbeatID,
				"clientId":  "tab-1",
			},
			handler:    (*Router).jukeboxSessionHeartbeat,
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "detach returns snapshot",
			method: http.MethodPost,
			path:   "/jukebox/session/detach",
			body: map[string]any{
				"sessionId": detachSnapshotID,
				"clientId":  "tab-1",
			},
			setup: func(t *testing.T) {
				t.Helper()
				_, err := api.playback.AttachSession(context.Background(), playback.AttachRequest{
					SessionID:  detachSnapshotID,
					User:       "admin",
					ClientID:   "tab-1",
					DeviceName: "pulse/test",
				})
				if err != nil {
					t.Fatalf("attach setup: %v", err)
				}
			},
			handler:    (*Router).jukeboxSessionDetach,
			wantStatus: http.StatusOK,
			assertJSON: func(t *testing.T, body []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got["sessionId"] != detachSnapshotID || got["ownerClientId"] != "tab-1" || got["deviceName"] != "pulse/test" {
					t.Fatalf("unexpected detach response: %#v", got)
				}
				if got["attached"] != false {
					t.Fatalf("expected attached=false, got %#v", got)
				}
			},
		},
		{
			name:   "heartbeat maps ownership to 403",
			method: http.MethodPost,
			path:   "/jukebox/session/heartbeat",
			body: map[string]any{
				"sessionId": forbiddenHeartbeatID,
				"clientId":  "tab-2",
			},
			setup: func(t *testing.T) {
				t.Helper()
				_, err := api.playback.AttachSession(context.Background(), playback.AttachRequest{
					SessionID:  forbiddenHeartbeatID,
					User:       "admin",
					ClientID:   "tab-1",
					DeviceName: "pulse/test",
				})
				if err != nil {
					t.Fatalf("attach setup: %v", err)
				}
			},
			handler:    (*Router).jukeboxSessionHeartbeat,
			wantStatus: http.StatusForbidden,
		},
		{
			name:   "detach maps ownership to 403",
			method: http.MethodPost,
			path:   "/jukebox/session/detach",
			body: map[string]any{
				"sessionId": forbiddenDetachID,
				"clientId":  "tab-2",
			},
			setup: func(t *testing.T) {
				t.Helper()
				_, err := api.playback.AttachSession(context.Background(), playback.AttachRequest{
					SessionID:  forbiddenDetachID,
					User:       "admin",
					ClientID:   "tab-1",
					DeviceName: "pulse/test",
				})
				if err != nil {
					t.Fatalf("attach setup: %v", err)
				}
			},
			handler:    (*Router).jukeboxSessionDetach,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "status validates session id",
			method:     http.MethodGet,
			path:       "/jukebox/session/status",
			handler:    (*Router).jukeboxSessionStatus,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "status maps not found to 404",
			method:     http.MethodGet,
			path:       "/jukebox/session/status?sessionId=" + missingStatusID,
			handler:    (*Router).jukeboxSessionStatus,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}

			ctx := request.WithUser(context.Background(), model.User{UserName: "admin"})
			var body *bytes.Reader
			if tt.body != nil {
				payload, err := json.Marshal(tt.body)
				if err != nil {
					t.Fatalf("marshal body: %v", err)
				}
				body = bytes.NewReader(payload)
			} else {
				body = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tt.method, tt.path, body).WithContext(ctx)
			res := httptest.NewRecorder()
			tt.handler(api, res, req)

			if res.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", res.Code, tt.wantStatus, res.Body.String())
			}
			if tt.assertJSON != nil {
				tt.assertJSON(t, res.Body.Bytes())
			}
		})
	}
}

func uniqueSessionID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
