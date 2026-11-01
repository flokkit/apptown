package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/poll"

	"github.com/flokkit/apptown/internal/apptown/secret"

	"github.com/flokkit/apptown/internal/apptown/integration"
)

func TestIntegrationAdaptersPreserveOrderingAndErrors(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	bindings := integration.NewWithClock(db, nil)
	catalog := NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()}))
	t.Cleanup(dbtest.NextStatementFailure(db, "-- name: Bindings :many"))
	if got, err := catalog.providers(context.Background()); err == nil || got != nil {
		t.Fatal("failed read returned inference providers")
	}
	if _, err := bindings.Upsert(t.Context(), "bad", "model", "test", `{"models":42}`, false); err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.providers(context.Background()); err == nil || got != nil || strings.Contains(err.Error(), "42") {
		t.Fatalf("malformed config must fail without exposing content: %v", err)
	}
}

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

// The model picker lists through the gateway's own resolution: a binding's
// default, its curated list and what its provider lists live. Concurrent
// readers of one binding ask its provider once, a curation change shows at
// once, and an unsaved binding's provider is asked with the same rules.
//
// Kills: a second resolver for the picker (its own key rules or cache); a
// discovery cache asked per reader; a stale snapshot hiding a saved change;
// a discovery cache aged by the real clock rather than AppTown's.
func TestTheModelListIsTheGatewaysOwn(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	var probes atomic.Int32
	// The provider holds its first answer until the test releases it, so the
	// concurrent readers overlap the probe.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		<-release
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("unexpected probe %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"live"}]}`))
	}))
	defer server.Close()
	bindings := integration.NewWithClock(db, nil)
	save := func(pattern string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"model": pattern, "baseUrl": server.URL, "providerDef": map[string]any{"apiKey": "provider-key"}})
		if _, err := bindings.Save(context.Background(), "provider", "model", "provider", string(raw), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	save("provider/first")
	catalog := NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()}))
	var clockMu sync.Mutex
	at := time.Now().Add(time.Hour)
	catalog.Now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return at }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if entries, unlisted, err := catalog.Models(context.Background()); err != nil || len(entries) != 2 || len(unlisted) != 0 {
				t.Errorf("models: %+v %v %v", entries, unlisted, err)
			}
		})
	}
	if !poll.Until(context.Background(), time.Minute, time.Millisecond, func() bool { return probes.Load() > 0 }) {
		t.Fatal("no reader asked the provider")
	}
	close(release)
	wg.Wait()
	if got := probes.Load(); got != 1 {
		t.Fatalf("concurrent readers made %d probes", got)
	}
	save("provider/second")
	entries, _, err := catalog.Models(context.Background())
	if err != nil || len(entries) != 2 || entries[0] != (ModelEntry{Pattern: "provider/second", Binding: "provider", Source: "default"}) || entries[1].Pattern != "provider/live" || entries[1].Source != "live" {
		t.Fatalf("a saved change shows at once: %+v %v", entries, err)
	}
	// The provider's list is kept a minute by AppTown's clock, then asked
	// again; no real time passes.
	clockMu.Lock()
	at = at.Add(2 * time.Minute)
	clockMu.Unlock()
	if _, _, err := catalog.Models(context.Background()); err != nil || probes.Load() != 2 {
		t.Fatalf("a list kept past its minute on AppTown's clock: probes %d, %v", probes.Load(), err)
	}
	ids, err := catalog.Discover(context.Background(), "unsaved", map[string]any{"baseUrl": server.URL, "providerDef": map[string]any{"apiKey": "provider-key"}})
	if err != nil || len(ids) != 1 || ids[0] != "live" {
		t.Fatalf("an unsaved binding: %v %v", ids, err)
	}
	if _, err := catalog.Discover(context.Background(), "unsaved", map[string]any{"baseUrl": server.URL, "api": "anthropic-messages"}); !errors.Is(err, errNoEndpoint) {
		t.Fatalf("a provider with no model list: %v", err)
	}
}

// A provider whose list cannot be read is unknown, not current and not empty:
// after one good answer a failing provider lists no live models, the picker
// names the binding with why, and the gateway's refusal of the once-listed
// model says the list could not be read. A first failure is named the same.
//
// Kills: reusing the earlier ids under a new time; a failed list read as an
// empty one.
func TestAnUnreadModelListIsNotACurrentOne(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	var down atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"live"}]}`))
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"baseUrl": server.URL, "providerDef": map[string]any{"apiKey": "provider-key"}})
	if _, err := integration.NewWithClock(db, nil).Save(context.Background(), "provider", "model", "provider", string(raw), "fixture"); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()}))
	at := time.Now()
	catalog.Now = func() time.Time { return at }
	if entries, unlisted, err := catalog.Models(t.Context()); err != nil || len(entries) != 1 || entries[0].Pattern != "provider/live" || len(unlisted) != 0 {
		t.Fatalf("the list answers: %+v %+v %v", entries, unlisted, err)
	}
	down.Store(true)
	at = at.Add(2 * time.Minute)
	entries, unlisted, err := catalog.Models(t.Context())
	if err != nil || len(entries) != 0 || len(unlisted) != 1 || unlisted[0].Binding != "provider" || !strings.Contains(unlisted[0].Error, "503") {
		t.Fatalf("a failed refresh: %+v %+v %v", entries, unlisted, err)
	}
	ps, err := catalog.providers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p := ps["provider"]; p.allows("live") || !strings.Contains(p.refusal("live").Error(), "could not be read") {
		t.Fatalf("the gateway after a failed refresh: allows %v, %v", p.allows("live"), p.refusal("live"))
	}
	first := NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()}))
	if entries, unlisted, err := first.Models(t.Context()); err != nil || len(entries) != 0 || len(unlisted) != 1 {
		t.Fatalf("a first failure: %+v %+v %v", entries, unlisted, err)
	}
}
