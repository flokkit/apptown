package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

const (
	providerHost = "orders-cloud-fgcfd.ondigitalocean.app"
	theRelease   = "web@r1"
)

// How one dial of the front door goes: the name is missing from DNS, it
// answered only addresses AppTown has no route to (an IPv6-only answer on
// the droplet, restore test R3-1), or it connects.
const (
	noSuchHost = "no such host"
	noRoute    = "no route"
	connects   = "connects"
)

// frontDoor is the app's front door as AppTown sees it: each dial goes
// as its script says (the last entry repeats), and each request that
// connects is answered with the next status (the last repeats); 200 at the
// release path answers this release. Every answer closes its connection,
// so each request dials again.
type frontDoor struct {
	mu              sync.Mutex
	dials           []string
	statuses        []int
	dialled, served int
	srv             *httptest.Server
}

func newFrontDoor(t *testing.T, dials []string, statuses ...int) *frontDoor {
	t.Helper()
	f := &frontDoor{dials: dials, statuses: statuses}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		status := f.statuses[min(f.served, len(f.statuses)-1)]
		f.served++
		f.mu.Unlock()
		w.Header().Set("Connection", "close")
		w.WriteHeader(status)
		if r.URL.Path == appcontract.ReleasePath && status == http.StatusOK {
			fmt.Fprint(w, theRelease)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *frontDoor) dial(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	step := f.dials[min(f.dialled, len(f.dials)-1)]
	f.dialled++
	f.mu.Unlock()
	switch step {
	case noSuchHost:
		return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: providerHost, IsNotFound: true}}
	case noRoute:
		return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
	}
	return (&net.Dialer{}).DialContext(ctx, network, f.srv.Listener.Addr().String())
}

func (f *frontDoor) counts() (dialled, served int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dialled, f.served
}

func repeat(step string, times int) []string {
	out := make([]string, times)
	for i := range out {
		out[i] = step
	}
	return out
}

// appAt answers one active app at address, the platform's own name for
// it, created at created (zero: the platform does not say when).
func appAt(t *testing.T, address string, created time.Time) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/apps/app-1/deployments/dep-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment": map[string]any{"id": "dep-1", "phase": "ACTIVE"}})
		case "/v2/apps/app-1":
			app := map[string]any{"id": "app-1", "live_url": address, "default_ingress": address,
				"active_deployment": map[string]any{"id": "dep-1", "phase": "ACTIVE"}}
			if !created.IsZero() {
				app["created_at"] = created.UTC().Format(time.RFC3339)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"app": app})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// checks names each of the front door's checks by what the checkpoint
// carries for it.
var checks = map[string]map[string]any{
	"readiness probe": {"readinessPath": "/healthz"},
	"release witness": {"release": theRelease},
}

// resume resumes the app's deployment through the real front door checks,
// over the front door's dial script.
func resume(t *testing.T, f *frontDoor, created time.Time, noted map[string]any) (string, error) {
	t.Helper()
	_, port, _ := net.SplitHostPort(f.srv.Listener.Addr().String())
	srv := appAt(t, "http://"+providerHost+":"+port, created)
	d := NewDriver(Dependencies{Slug: "orders", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: srv.URL, HTTP: srv.Client()}, Config{Region: "syd"}, nil
	}}, Options{PollInterval: time.Millisecond, Dial: f.dial})
	ids := map[string]any{"appId": "app-1", "appName": "orders-cloud", "deploymentId": "dep-1"}
	for k, v := range noted {
		ids[k] = v
	}
	// Every check here ends in milliseconds; one that does not has lost
	// count of its tries.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var log []string
	_, err := d.Resume(ctx, deploy.Placement{Name: "cloud"}, 1, ids, func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	return strings.Join(log, "\n"), err
}

// A new app's provider-issued name can be missing from DNS, or answer only
// addresses AppTown has no route to, for the zone's 30-minute
// negative-cache time and more (restore tests RT-L2 and R3-1: the probes
// and recoveries were spent inside that window, and a person had to press
// Check again). Inside ProviderNameGrace such a request is "not yet": it
// is asked again and does not spend one of the check's tries, including
// 31 minutes after the app was created.
func TestANewAppsNameNotAnsweringIsNotYetNotAFailure(t *testing.T) {
	t.Parallel()
	for check, noted := range checks {
		for _, step := range []string{noSuchHost, noRoute} {
			for _, age := range []time.Duration{2 * time.Minute, 31 * time.Minute} {
				f := newFrontDoor(t, append(repeat(step, 8), connects), http.StatusOK)
				log, err := resume(t, f, time.Now().Add(-age), noted)
				if _, served := f.counts(); err != nil || served != 1 || !strings.Contains(log, "does not answer yet") {
					t.Fatalf("%s, %s, created %s ago: %v (answered %d)\n%s", check, step, age, err, served, log)
				}
			}
		}
	}
}

// Only the requests the name did not answer go uncounted: a front door
// that answers is judged as before. A transient 503 among DNS misses keeps
// the check's allowance and the next good answer finishes it at once,
// while six answers that are not good fail the check however many misses
// fall between them.
func TestAnsweredTriesKeepTheirBudgetAmongMisses(t *testing.T) {
	t.Parallel()
	for check, noted := range checks {
		f := newFrontDoor(t, append(repeat(noSuchHost, 5), connects), http.StatusServiceUnavailable, http.StatusOK)
		log, err := resume(t, f, time.Now().Add(-5*time.Minute), noted)
		if _, served := f.counts(); err != nil || served != 2 {
			t.Fatalf("%s: misses, a 503, then a good answer: %v (answered %d)\n%s", check, err, served, log)
		}
		f = newFrontDoor(t, []string{noSuchHost, connects, noRoute, connects, noSuchHost, noSuchHost, connects, connects, noRoute, connects, connects}, http.StatusServiceUnavailable)
		log, err = resume(t, f, time.Now().Add(-5*time.Minute), noted)
		if _, served := f.counts(); err == nil || served != 6 {
			t.Fatalf("%s: six 503s among misses: %v (answered %d)\n%s", check, err, served, log)
		}
	}
}

// After the grace, or for an app whose creation time is unknown, a name
// that does not answer spends the check's tries as before: the probe's
// failure leaves the release uncertain, and the witness's fails it.
func TestAProviderNameStillMissingAfterTheGraceFailsAsBefore(t *testing.T) {
	t.Parallel()
	for name, created := range map[string]time.Time{
		"past the grace":   time.Now().Add(-ProviderNameGrace - time.Minute),
		"creation unknown": {},
	} {
		for check, noted := range checks {
			f := newFrontDoor(t, []string{noSuchHost}, http.StatusOK)
			log, err := resume(t, f, created, noted)
			if err == nil || !strings.Contains(err.Error(), "no such host") || deploy.IsTerminal(err) != (check == "release witness") {
				t.Fatalf("%s, %s: %v", name, check, err)
			}
			if dialled, _ := f.counts(); strings.Contains(log, "does not answer yet") || dialled != 6 {
				t.Fatalf("%s, %s: the name was waited for (dialled %d)\n%s", name, check, dialled, log)
			}
		}
	}
}

// fakeDNS answers "no such host" for its first missing lookups, then the
// name; it counts the lookups.
type fakeDNS struct {
	mu      sync.Mutex
	missing int
	asked   int
}

func (f *fakeDNS) resolve(_ context.Context, host string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	if f.asked <= f.missing {
		return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return nil
}

// A gated copy's front door is not asked by the driver: inside the grace,
// it waits for the name to be in DNS before handing the address to the
// checks through the gate.
func TestAGatedCopyWaitsForItsNameBeforeTheChecks(t *testing.T) {
	t.Parallel()
	dns := &fakeDNS{missing: 4}
	srv := appAt(t, "https://"+providerHost, time.Now().Add(-2*time.Minute))
	d := NewDriver(Dependencies{Slug: "orders", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: srv.URL, HTTP: srv.Client()}, Config{Region: "syd"}, nil
	}}, Options{PollInterval: time.Millisecond, Resolve: dns.resolve})
	var log []string
	_, err := d.Resume(context.Background(), deploy.Placement{Name: "cloud", Gate: &deploy.Gate{}}, 1,
		map[string]any{"appId": "app-1", "appName": "orders-cloud", "deploymentId": "dep-1"}, func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	if err != nil || dns.asked != 5 || !strings.Contains(strings.Join(log, "\n"), "is in DNS now") {
		t.Fatalf("a gated copy's name: %v, asked DNS %d times\n%s", err, dns.asked, strings.Join(log, "\n"))
	}
}

// Only "no such host" for the platform's own name is waited for: a
// resolver that fails another way (it timed out, or the server failed), or
// a name the platform did not issue (a custom domain), is not, and the
// checks through the gate say what happened.
func TestOnlyNoSuchHostForThePlatformsNameIsWaitedFor(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		url    string
		answer *net.DNSError
		asks   int
	}{
		"resolver failing": {"https://" + providerHost, &net.DNSError{Err: "server misbehaving", Name: providerHost, IsTemporary: true}, 1},
		"custom domain":    {"https://orders.example.com", &net.DNSError{Err: "no such host", Name: "orders.example.com", IsNotFound: true}, 0},
	} {
		asked := 0
		d := NewDriver(Dependencies{}, Options{PollInterval: time.Millisecond, Resolve: func(context.Context, string) error {
			asked++
			return tc.answer
		}})
		// A wait would outlast this; the grace itself is minutes.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var log []string
		err := d.awaitProviderName(ctx, tc.url, providerNameGrace(tc.url, time.Now()), func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
		cancel()
		if err != nil || asked != tc.asks || len(log) != 0 {
			t.Fatalf("%s was waited for: %v, asked DNS %d times, log %v", name, err, asked, log)
		}
	}
}

// The wait stops when the attempt is cancelled.
func TestWaitingForAProviderNameStopsWithTheAttempt(t *testing.T) {
	t.Parallel()
	d := NewDriver(Dependencies{}, Options{PollInterval: time.Hour, Resolve: (&fakeDNS{missing: 1000}).resolve})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.awaitProviderName(ctx, "https://"+providerHost, time.Now().Add(time.Minute), func(string, ...any) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait: %v", err)
	}
}
