package imagelines

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/imageline"
)

var suite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(suite.Run(m)) }
func TestReadingsReuseRefreshAndRefuseExpiredAnswers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	s := New(suite.New(t), func() time.Time { return now }, nil)
	calls := 0
	down := false
	s.newest = func(context.Context, string, imageline.Scope, imageline.Access) (imageline.Answer, error) {
		calls++
		if down {
			return imageline.Answer{}, errors.New("registry offline")
		}
		return imageline.Answer{Tag: "python:3.12.9-slim", Digest: "sha256:" + strings.Repeat("a", 64), Registry: "registry.test"}, nil
	}
	if _, known, err := s.Cached(t.Context(), "python:3.12-slim", imageline.Line); err != nil || known || calls != 0 {
		t.Fatalf("missing cached answer contacted registry: %t %d %v", known, calls, err)
	}
	first, err := s.Answer(t.Context(), "python:3.12-slim", imageline.Line)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Answer(t.Context(), "python:3.12-slim", imageline.Line)
	if err != nil || fresh.ReadAt != first.ReadAt || calls != 1 {
		t.Fatalf("reuse %+v %d %v", fresh, calls, err)
	}
	if got, known, err := s.Cached(t.Context(), "python:3.12-slim", imageline.Line); err != nil || !known || got.Image() != first.Image() || calls != 1 {
		t.Fatalf("cached read: %+v %t %d %v", got, known, calls, err)
	}
	now = now.Add(25 * time.Hour)
	refresh, err := s.Answer(t.Context(), "python:3.12-slim", imageline.Line)
	if err != nil || !refresh.ReadAt.After(first.ReadAt) || calls != 2 {
		t.Fatalf("refresh %+v %d %v", refresh, calls, err)
	}
	down = true
	now = now.Add(3 * 24 * time.Hour)
	stale, err := s.Answer(t.Context(), "python:3.12-slim", imageline.Line)
	if err != nil || !stale.Stale || stale.ReadAt != refresh.ReadAt || stale.Error != "registry offline" {
		t.Fatalf("stale %+v %v", stale, err)
	}
	now = now.Add(5 * 24 * time.Hour)
	before := calls
	if _, known, err := s.Cached(t.Context(), "python:3.12-slim", imageline.Line); err != nil || known || calls != before {
		t.Fatalf("expired cached answer contacted registry: %t %d %v", known, calls, err)
	}
	if _, err := s.Answer(t.Context(), "python:3.12-slim", imageline.Line); err == nil {
		t.Fatal("8-day reading used")
	}
	if _, err := s.Answer(t.Context(), "node:24-slim", imageline.Line); err == nil {
		t.Fatal("absent reading used")
	}
}
func TestConcurrentCallersShareOneLookup(t *testing.T) {
	t.Parallel()
	s := New(suite.New(t), nil, nil)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	s.newest = func(context.Context, string, imageline.Scope, imageline.Access) (imageline.Answer, error) {
		calls.Add(1)
		close(started)
		<-release
		return imageline.Answer{Tag: "caddy:2.11.9-alpine", Digest: "sha256:a", Registry: "mirror"}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Answer(t.Context(), "caddy:2-alpine", imageline.Line); err != nil {
				t.Error(err)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
