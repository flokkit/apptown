package digitalocean

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The front door is judged by the readiness rule every target applies
// (appcontract.Ready): a redirect is an answer below 400 and is not
// followed; a 404 with nothing declared is not up. The front door used to
// follow the redirect (here to a 404) and want below 300.
func TestTheFrontDoorIsJudgedByTheReadinessRule(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/missing", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	quiet := func(string, ...any) {}
	if err := probeFrontDoor(ctx, srv.Client(), srv.URL+"/moved", 0, past, quiet, time.Millisecond, time.Now); err != nil {
		t.Errorf("a redirect at the health path: %v, want up", err)
	}
	if err := probeFrontDoor(ctx, srv.Client(), srv.URL+"/missing", 0, past, quiet, time.Millisecond, time.Now); err == nil {
		t.Error("a 404 with nothing declared: up, want refused")
	}
	if err := probeFrontDoor(ctx, srv.Client(), srv.URL+"/missing", 404, past, quiet, time.Millisecond, time.Now); err != nil {
		t.Errorf("a declared 404: %v, want up", err)
	}
	d := &Driver{frontDoor: srv.Client()}
	allowed := func(context.Context) error { return nil }
	if ok, err := d.sampleFrontDoor(ctx, srv.URL+"/moved", 0, "", allowed); err != nil || !ok {
		t.Errorf("maintained health of a redirect: %v %v, want healthy", ok, err)
	}
}
