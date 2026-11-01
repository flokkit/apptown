package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// A preview's build images are removed without force: one a container
// still uses stays and is no failure; a removal that fails otherwise, and
// a listing that fails, are named.
func TestRemoveBuildImagesNeverForcesAndNamesFailures(t *testing.T) {
	t.Parallel()
	const own, part, inUse, broken = "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-web:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-db:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-x:latest"
	daemon := newDockerFake()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		call := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(call, "image ls"):
			return []byte(strings.Join([]string{own, part, inUse, broken}, "\n")), nil, true
		case call == "rmi "+inUse:
			return []byte(`Error response from daemon: conflict: unable to remove repository reference "` + inUse + `" (must force) - container 0123 is using its referenced image 4567`), errors.New("exit status 1"), true
		case call == "rmi "+broken:
			return []byte("Error response from daemon: something else"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	service := &Service{hosts: deployment.NewHosts(nil, nil, deployment.HostSettings{Scope: docker.Scope{Client: daemon}})}
	failed := service.removeLocalPreview(context.Background(), "apptown-preview-run-00000000-0001-7000-8000-000000000000")
	if failed == nil || !strings.Contains(failed.Error(), broken) {
		t.Fatalf("failure: %v", failed)
	}
	var removed []string
	for _, call := range daemon.Matching("rmi") {
		if slices.Contains(call, "-f") || slices.Contains(call, "--force") {
			t.Fatalf("an image was forced: %v", call)
		}
		removed = append(removed, call[len(call)-1])
	}
	if !slices.Equal(removed, []string{own, part, inUse, broken}) {
		t.Fatalf("asked to remove %q", removed)
	}

	unlisted := newDockerFake()
	unlisted.SetReply(func([]string) ([]byte, error, bool) { return []byte("daemon gone"), errors.New("exit status 1"), true })
	service.hosts = deployment.NewHosts(nil, nil, deployment.HostSettings{Scope: docker.Scope{Client: unlisted}})
	if failed := service.removeLocalPreview(context.Background(), "apptown-preview-run-00000000-0001-7000-8000-000000000000"); failed == nil {
		t.Fatalf("a listing that failed: %q", failed)
	}
}

// The retirement owner needs only retained IDs: no run/change rows exist.
// Failure does not suppress the other disposable effects, or become success.
func TestPermanentPreviewRetirementOwnsSnapshotsAndReportsFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"run", "change"} {
		for _, failure := range []string{"none", "worker", "local", "view", "absent-local"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				w := newWorld(t, "", "")
				worker := &fakePreviewWorker{}
				s := w.service(Dependencies{})
				if failure == "worker" || failure == "absent-local" {
					s.worker = worker
				}
				if failure == "worker" {
					worker.err = errors.New("worker unavailable")
				}
				if failure == "absent-local" {
					w.reconfigure(s, func(h *deployment.HostSettings) { h.Scope.Client = docker.Refusing{} }, nil)
				}
				id := idstest.N(901)
				name := "apptown-preview-" + kind + "-" + id.String()
				s.Viewed(t.Context(), name)
				snapshot := filepath.Join(w.dir, "workspaces", "asof-"+kind+"-"+id.String())
				keep := []string{filepath.Join(w.dir, "workspaces", kind+"-"+id.String()), filepath.Join(w.dir, "workspaces", "asof-"+kind+"-neighbor")}
				for _, dir := range append(keep, snapshot) {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				w.client.SetVolume("application-data", true)
				if failure == "local" {
					w.client.SetReply(func(args []string) ([]byte, error, bool) {
						if len(args) > 0 && args[0] == "rm" {
							return []byte("denied"), errors.New("denied"), true
						}
						return nil, nil, false
					})
				}
				if failure == "view" {
					t.Cleanup(dbtest.NextStatementFailure(w.db, "-- name: ForgetPreviewView"))
				}
				retire := func() error {
					if kind == "run" {
						return s.RemoveRunForGood(t.Context(), id)
					}
					return s.RemoveChangeForGood(t.Context(), id)
				}
				err := retire()
				wantFailure := failure == "worker" || failure == "local" || failure == "view"
				if (err != nil) != wantFailure {
					t.Fatalf("retirement failure=%s: %v", failure, err)
				}
				if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
					t.Fatalf("preview owner left snapshot: %v", err)
				}
				for _, dir := range keep {
					if _, err := os.Stat(dir); err != nil {
						t.Fatalf("unowned workspace removed: %s: %v", dir, err)
					}
				}
				if failure != "absent-local" && len(w.client.Matching("image ls")) == 0 {
					t.Fatal("failed earlier effect suppressed local images")
				}
				for _, call := range w.client.Matching("volume rm") {
					if slices.Contains(call, "application-data") {
						t.Fatalf("application data removal: %v", call)
					}
				}
				if failure == "worker" && lookAt(t, s, name).IsZero() {
					t.Fatal("worker failure forgot its look")
				}
				worker.err = nil
				w.client.SetReply(nil)
				if err := retire(); err != nil {
					t.Fatalf("retained IDs could not retry: %v", err)
				}
				if !lookAt(t, s, name).IsZero() {
					t.Fatal("successful retry retained its look")
				}
				if s.PreviewsOnWorker() && !slices.Contains(worker.images, name) {
					t.Fatal("worker build images were not retired")
				}
			})
		}
	}
}

func TestPermanentStepRetirementAttemptsEveryFrozenStack(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	first, second := idstest.N(801), idstest.N(802)
	denied := errors.New("first stack unavailable")
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), first.String()) {
			return []byte("denied"), denied, true
		}
		return nil, nil, false
	})
	s := &Service{hosts: deployment.NewHosts(nil, nil, deployment.HostSettings{Scope: docker.Scope{Client: daemon}})}
	if err := s.RemoveRunStepsForGood(t.Context(), []ids.ID{first, second}); err == nil {
		t.Fatal("failed stack retirement succeeded")
	}
	found := false
	for _, call := range daemon.Matching("ps") {
		if strings.Contains(strings.Join(call, " "), second.String()) {
			found = true
		}
	}
	if !found {
		t.Fatal("first failure suppressed later frozen stack")
	}
	daemon.SetReply(nil)
	if err := s.RemoveRunStepsForGood(t.Context(), []ids.ID{first, second}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func newDockerFake() *dockerfake.Daemon {
	return dockerfake.New()
}
