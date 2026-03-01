package nativeapi

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
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
