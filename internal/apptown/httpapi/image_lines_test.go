package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
)

type imageReading struct {
	err   error
	ref   string
	scope imageline.Scope
}

func (r *imageReading) Answer(_ context.Context, ref string, scope imageline.Scope) (imagelines.Reading, error) {
	r.ref, r.scope = ref, scope
	return imagelines.Reading{Answer: imageline.Answer{Tag: "caddy:2.11.9-alpine", Digest: "sha256:" + strings.Repeat("a", 64), Registry: "mirror.test"}, ReadAt: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), Stale: true, Error: "registry offline"}, r.err
}
func TestImageLinesHTTPKeepsTheRecordedAnswerAndRefusal(t *testing.T) {
	t.Parallel()
	resolver := &imageReading{}
	h := ImageLines{Resolver: resolver}
	w := httptest.NewRecorder()
	h.Newest(w, httptest.NewRequest("GET", "/api/v1/image-lines?ref=caddy:2-alpine&scope=minor", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"stale":true`) || !strings.Contains(w.Body.String(), `"error":"registry offline"`) || !strings.Contains(w.Body.String(), `"readAt":"2026-11-17T00:00:00Z"`) || resolver.ref != "caddy:2-alpine" || resolver.scope != imageline.Minor {
		t.Fatalf("%d %s %+v", w.Code, w.Body, resolver)
	}
	resolver.err = errors.New("registry refused the request")
	w = httptest.NewRecorder()
	h.Newest(w, httptest.NewRequest("GET", "/api/v1/image-lines?ref=caddy:2-alpine&scope=line", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), resolver.err.Error()) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, query := range []string{"?scope=line", "?ref=caddy:2&scope=anything"} {
		w = httptest.NewRecorder()
		h.Newest(w, httptest.NewRequest("GET", "/api/v1/image-lines"+query, nil))
		if w.Code != 400 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
