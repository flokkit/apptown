package digitalocean

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/poll"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type coldTransport func(*http.Request) (*http.Response, error)

func (f coldTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExplicitAppPlatformColdReadinessCountsExactDeploymentSamples(t *testing.T) {
	for _, mode := range []string{"healthy", "miss", "replacement", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probes := 0
				var starts []time.Time
				api := coldTransport(func(r *http.Request) (*http.Response, error) {
					id := "selected"
					if mode == "replacement" && probes > 0 {
						id = "different"
					}
					body, _ := json.Marshal(map[string]any{"app": map[string]any{"id": "app", "live_url": "https://app.example", "active_deployment": map[string]any{"id": id, "phase": "ACTIVE"}}})
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
				})
				front := coldTransport(func(r *http.Request) (*http.Response, error) {
					probes++
					starts = append(starts, time.Now())
					status := 204
					if mode == "miss" && probes == 1 {
						status = 503
					}
					if mode == "deadline" {
						_ = poll.Wait(context.Background(), 2*time.Second)
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
				})
				d := NewDriver(Dependencies{}, Options{})
				d.frontDoor = &http.Client{Transport: front}
				// Bind the legacy cold-owner seam too: the same regression must stay
				// on the fake transport when exercised against its previous implementation.
				d.options.ProbeFrontDoor = func(ctx context.Context, url string, expect int, until time.Time, logf func(string, ...any)) error {
					return probeFrontDoor(ctx, d.frontDoor, url, expect, until, logf, time.Second, time.Now)
				}
				c := &Client{Base: "https://api.example", Token: "owned", HTTP: &http.Client{Transport: api}}
				q := readinessSnapshot{Path: "/ready", Expect: 204, Policy: &appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second, Timeout: time.Second, SuccessThreshold: 2, FailureThreshold: 1}}
				ctx := t.Context()
				cancel := func() {}
				if mode == "deadline" {
					ctx, cancel = context.WithTimeout(ctx, 1500*time.Millisecond)
				}
				defer cancel()
				observed, err := d.finish(ctx, c, "app", "owned", &doDeployment{ID: "selected", Phase: "ACTIVE"}, 1, q, "", false, func(string, ...any) {})
				if mode == "replacement" || mode == "deadline" {
					if err == nil {
						t.Fatal("unwitnessed cold readiness accepted")
					}
					return
				}
				want := 2
				if mode == "miss" {
					want = 3
				}
				if err != nil || probes != want || observed.Health != "healthy" {
					t.Fatalf("probes=%d want%d err=%v", probes, want, err)
				}
				for i := 1; i < len(starts); i++ {
					if starts[i].Sub(starts[i-1]) != 5*time.Second {
						t.Fatal("policy cadence ignored")
					}
				}
			})
		})
	}
}
