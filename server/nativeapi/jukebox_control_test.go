package nativeapi

import (
	"context"
	"net/http"
	"testing"

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

func TestJukeboxSessionPlaybackAPIsCompile(t *testing.T) {
	var api Router
	if api.playback == nil {
		return
	}

	_, _ = api.playback.AttachSession(context.Background(), playback.AttachRequest{SessionID: "s1", ClientID: "tab-1"})
	_, _ = api.playback.HeartbeatSession(context.Background(), "s1", "tab-1")
	_ = api.playback.DetachSession(context.Background(), "s1", "tab-1")
	_, _ = api.playback.SessionStatus(context.Background(), "s1")
}
