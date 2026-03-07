package nativeapi

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
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
