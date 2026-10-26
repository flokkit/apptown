package digitalocean

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// stepping is a clock far from the real one that moves on by step at each
// reading, so a deadline set on it passes after a few readings.
func stepping(step time.Duration) func() time.Time {
	var mu sync.Mutex
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(step)
		return at
	}
}

// The platform's new name's grace is judged on the driver's clock: once it
// says the grace is over, the name is handed on without waiting.
func TestTheProviderNamesGraceEndsByTheDriversClock(t *testing.T) {
	t.Parallel()
	d := NewDriver(Dependencies{}, Options{PollInterval: time.Millisecond, Now: stepping(0),
		Resolve: (&fakeDNS{missing: 1000}).resolve})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var log []string
	ended := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) // past on the driver's clock, ahead of the real one
	err := d.awaitProviderName(ctx, "https://"+providerHost, ended, func(f string, a ...any) { log = append(log, f) })
	if err != nil || len(log) != 1 || !strings.Contains(log[0], "still not in DNS") {
		t.Fatalf("a grace over on the driver's clock: %v, log %q", err, log)
	}
}

// A front door try the new name did not answer counts once the grace is
// over on the driver's clock.
func TestAFrontDoorTryCountsOnceTheGraceIsOverByTheDriversClock(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "no such host", Name: providerHost, IsNotFound: true}
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ended := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	err := probeFrontDoor(ctx, client, "https://"+providerHost+"/up", 0, ended, func(string, ...any) {}, time.Millisecond, stepping(0))
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("six tries after the grace, on the driver's clock: %v", err)
	}
}

// A cluster that is not online within 15 minutes on the driver's clock is
// given up.
func TestAClusterIsGivenUpAfter15MinutesByTheDriversClock(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/databases":
			_, _ = w.Write([]byte(`{"databases":[]}`))
		default:
			_, _ = w.Write([]byte(`{"database":{"id":"db-new","name":"forge-demo-main","status":"creating"}}`))
		}
	}))
	defer server.Close()
	driver := NewDriver(Dependencies{
		Client: func(context.Context) (*Client, Config, error) {
			return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd"}, nil
		},
		Slug:  "demo",
		Needs: &failingNeedRecorder{},
	}, Options{DBPoll: time.Millisecond, Now: stepping(10 * time.Minute)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := driver.FulfilNeed(ctx, deploy.Placement{ID: idstest.N(7), Name: "main"}, "postgres", nil, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "after 15 minutes") {
		t.Fatalf("a cluster creating for 20 minutes on the driver's clock: %v", err)
	}
}

// A deployment not active within 20 minutes on the driver's clock is given
// up.
func TestADeploymentIsGivenUpAfter20MinutesByTheDriversClock(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deployment":{"id":"dep-1","phase":"BUILDING"}}`))
	}))
	defer server.Close()
	d := NewDriver(Dependencies{}, Options{PollInterval: time.Millisecond, Now: stepping(15 * time.Minute)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.WaitDeployment(ctx, &Client{Base: server.URL, HTTP: server.Client()}, "app-1", "dep-1", func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "within 20 minutes") {
		t.Fatalf("a deployment building for 30 minutes on the driver's clock: %v", err)
	}
}
