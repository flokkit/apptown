package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// A stack whose services declare no healthcheck gets a blind grace before
// the app starts; a boot whose lifetime ends during it does not sit out the
// rest.
func TestAStacksGraceEndsWithTheBootsLifetime(t *testing.T) {
	t.Parallel()
	life, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dk := Scope{Client: &fakeClient{}}.WithLifetime(life)
	done := make(chan struct{})
	go func() {
		waitStackReady(life, dk, []deploy.ServiceSpec{{Name: "redis", Image: "redis:7"}}, "pv1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second): // the grace is 4 s
		t.Fatal("the stack's grace outlived the boot's lifetime")
	}
}

// The probe's settle beat between an app's first answer and its second ends
// when the boot's lifetime does, and the app is not called up.
func TestAProbesSettleEndsWithTheBootsLifetime(t *testing.T) {
	t.Parallel()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The app answers once, and the boot ends as it does: the probe is
	// then in its settle beat, an hour long.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()
	dk := Scope{Client: &fakeClient{out: "true"}}.WithLifetime(life)
	type answer struct {
		up   bool
		tail string
	}
	done := make(chan answer, 1)
	go func() {
		up, tail := (&Probe{Settle: time.Hour}).Check(life, dk, "apptown-preview-1", app.URL, time.Hour, renderedProbe{Path: "/up"})
		done <- answer{up, tail}
	}()
	select {
	case got := <-done:
		if got.up {
			t.Fatal("a boot that ended during the settle beat was called up")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the probe's settle beat outlived the boot's lifetime")
	}
}
