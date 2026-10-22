package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPackAssistanceWithoutModelSelectsOnlyOfferedPacks(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	h := NewPackAssistance(f.ways, noBriefModel{}, nil)
	for _, handler := range []http.HandlerFunc{h.PackInterview} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"description":"a game"}`)))
		if w.Code != 422 {
			t.Fatalf("empty library: %d %s", w.Code, w.Body.String())
		}
	}
	for _, name := range []string{"alpha", "go-small"} {
		if _, err := f.ways.PublishPack(t.Context(), name, name, `{}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct{ disable, want string }{{"", "go-small"}, {"go-small", "alpha"}} {
		if step.disable != "" {
			if err := f.ways.OfferPack(t.Context(), step.disable, false, "bls"); err != nil {
				t.Fatal(err)
			}
		}
		for _, handler := range []struct {
			name string
			call http.HandlerFunc
		}{{"interview", h.PackInterview}} {
			w := httptest.NewRecorder()
			handler.call(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"description":"a game"}`)))
			var out struct {
				Pack, Why string
				Decided   bool
				Questions []string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Pack != step.want || out.Why == "" || (handler.name == "interview" && (!out.Decided || len(out.Questions) > 0)) {
				t.Fatalf("fallback: %d %s %v", w.Code, w.Body.String(), err)
			}
			bad := httptest.NewRecorder()
			handler.call(bad, httptest.NewRequest("POST", "/", strings.NewReader(`{`)))
			if bad.Code != 400 {
				t.Fatalf("malformed request: %d %s", bad.Code, bad.Body.String())
			}
		}
	}
}
