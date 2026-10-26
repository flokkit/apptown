package instanceupdate

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/sigstore/sigstore-go/pkg/root"
)

type authorityFunc func(context.Context, access.Permission) (access.Caller, error)

func (f authorityFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return f(ctx, p)
}
func permitted(_ context.Context, p access.Permission) (access.Caller, error) {
	if p.Capability != access.CapInstanceUpdates {
		return access.Caller{}, errors.New("wrong capability")
	}
	return access.Caller{}, nil
}

func TestStatusAndChecksRequireCurrentAuthorityBeforeEffects(t *testing.T) {
	allowed := true
	calls := 0
	now := time.Now()
	s, err := New(Options{DataDir: t.TempDir(), Build: "v1.0.0", Platform: "linux/amd64", Now: func() time.Time { return now }, Authority: authorityFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
		if !allowed {
			return access.Caller{}, access.ErrPrincipalRequired
		}
		return permitted(ctx, p)
	}), Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, "[]"), nil })})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := s.Status(context.Background()); err != nil || status.State != "unknown" || calls != 0 {
		t.Fatalf("GET performed discovery: %+v %v %d", status, err, calls)
	}
	allowed = false
	if _, err := s.Check(context.Background()); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("withdrawn authority: %v", err)
	}
	if _, err := s.Status(context.Background()); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("withdrawn read authority: %v", err)
	}
	if _, err := os.Stat(s.cache.dir); !os.IsNotExist(err) || calls != 0 {
		t.Fatalf("refused caller caused effects: %v %d", err, calls)
	}
	allowed = true
	status, err := s.Check(context.Background())
	if err != nil || status.State != "no-release" || calls != 1 {
		t.Fatalf("complete empty response: %+v %v %d", status, err, calls)
	}
	if _, err := s.Check(context.Background()); err == nil || calls != 1 {
		t.Fatalf("cooldown: %v %d", err, calls)
	}
	now = now.Add(27 * time.Hour)
	if status, err := s.Status(context.Background()); err != nil || status.State != "stale" || calls != 1 {
		t.Fatalf("old observations: %+v %v %d", status, err, calls)
	}
}

func TestVerifiedCandidateSurvivesFailedChecksAndRestartButNotCorruptEvidence(t *testing.T) {
	f := newSignedFixture(t)
	now := time.Now().UTC()
	failed := false
	calls := 0
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if failed {
			return response(503, "private attacker-controlled text"), nil
		}
		if r.URL.Host == "api.github.com" {
			return response(200, `[{"tag_name":"v1.1.0"}]`), nil
		}
		bodies := map[string][]byte{ManifestName: f.manifest, ChecksumsName: f.checksums, BundleName: f.signature, NotesName: f.notes}
		body, ok := bodies[filepath.Base(r.URL.Path)]
		if !ok {
			t.Fatalf("unexpected public fetch %s", r.URL)
		}
		return response(200, string(body)), nil
	})
	options := Options{DataDir: t.TempDir(), Build: "custom", Platform: "linux/amd64", Now: func() time.Time { return now }, Authority: authorityFunc(permitted), Transport: transport}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	s.source.verify.trust = func(context.Context) (root.TrustedMaterial, error) { return f.trust, nil }
	first, err := s.Check(context.Background())
	if err != nil || first.State != "available" || first.Candidate == nil || first.AutomaticReplacement {
		t.Fatalf("verified candidate: %+v %v", first, err)
	}
	success := *first.LastSuccess
	oldCalls := calls
	s, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.Status(context.Background())
	if err != nil || held.Candidate.Version != "v1.1.0" || calls != oldCalls {
		t.Fatalf("restart or GET lost evidence: %+v %v", held, err)
	}
	failed = true
	now = now.Add(2 * time.Minute)
	status, err := s.Check(context.Background())
	if err == nil || status.State != "stale" || status.Candidate == nil || !status.LastSuccess.Equal(success) || !status.LastAttempt.Equal(now) || strings.Contains(status.Reason, "attacker") {
		t.Fatalf("failed check erased or misrepresented success: %+v %v", status, err)
	}
	if err := os.Remove(filepath.Join(s.cache.dir, f.tag+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(context.Background()); err == nil {
		t.Fatal("missing proof still presented as verified")
	}
}

func TestReleaseIdentityIsImmutableAcrossRestarts(t *testing.T) {
	f := newSignedFixture(t)
	release, err := f.verify()
	if err != nil {
		t.Fatal(err)
	}
	store := cacheStore{dir: filepath.Join(t.TempDir(), "instance-updates")}
	if err := store.retain(release); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, f.tag+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := release
	changed.ManifestBytes = append([]byte(nil), release.ManifestBytes...)
	changed.ManifestBytes = append(changed.ManifestBytes, ' ')
	restarted := cacheStore{dir: store.dir}
	if err := restarted.retain(changed); err == nil {
		t.Fatal("changed tag identity overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("original proof modified: %v", err)
	}
	if err := restarted.retain(release); err != nil {
		t.Fatalf("same identity refused: %v", err)
	}
}

func TestCheckCancellationReleasesSingleFlightAndPreservesLastSuccess(t *testing.T) {
	now := time.Now().UTC()
	block := false
	started := make(chan struct{})
	s, err := New(Options{DataDir: t.TempDir(), Build: "v1.0.0", Platform: "linux/amd64", Now: func() time.Time { return now }, Authority: authorityFunc(permitted), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if block {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(200, "[]"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	block = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Check(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("check did not start")
	}
	if status, err := s.Status(context.Background()); err != nil || status.State != "checking" {
		t.Fatalf("in-flight status: %+v %v", status, err)
	}
	if _, err := s.Check(context.Background()); err == nil {
		t.Fatal("concurrent check admitted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled check did not settle")
	}
	held, err := s.Status(context.Background())
	if err != nil || held.State != "stale" || !held.LastSuccess.Equal(*previous.LastSuccess) {
		t.Fatalf("canceled check lost prior success: %+v %v", held, err)
	}
	now = now.Add(2 * time.Minute)
	block = false
	if status, err := s.Check(context.Background()); err != nil || status.State != "no-release" {
		t.Fatalf("single-flight not released: %+v %v", status, err)
	}
}

func TestPersistenceFailuresCannotReportSuccessfulCheck(t *testing.T) {
	for _, stage := range []string{"before fetch", "after fetch"} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			calls := 0
			var s *Service
			var err error
			s, err = New(Options{DataDir: directory, Build: "v1.0.0", Platform: "linux/amd64", Authority: authorityFunc(permitted), Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				// A directory occupying the state destination makes atomic replacement
				// fail, independently of permissions or the effective test user.
				path := filepath.Join(s.cache.dir, "state.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				return response(200, "[]"), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before fetch" {
				if err := os.WriteFile(s.cache.dir, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Check(context.Background()); err == nil {
				t.Fatal("persistence failure reported success")
			}
			wantCalls := 1
			if stage == "before fetch" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("fetches %d want %d", calls, wantCalls)
			}
			if _, err := s.cache.read("stable"); err == nil {
				t.Fatal("failure left a readable successful observation")
			}
		})
	}
}
