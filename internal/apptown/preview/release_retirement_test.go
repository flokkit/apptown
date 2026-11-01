package preview

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
)

// Boot actual recorded releases before retirement. A second release of the
// same project distinguishes exact retained identity from project-prefix cleanup.
func TestReleaseRetirementRemovesBootedResourcesAndPreservesNeighbor(t *testing.T) {
	for _, where := range []string{"worker", "local"} {
		for _, failure := range []string{"none", "worker", "local", "view"} {
			t.Run(where+"/"+failure, func(t *testing.T) {
				s, remote, _, bundle, published, recorded := releasePreviewWorld(t)
				daemon := s.hosts.Local().Client.(*dockerfake.Daemon)
				number := published
				if where == "local" {
					number = recorded
				}
				rel, err := s.releases.ByNumber(t.Context(), bundle.ProjectID, number)
				if err != nil {
					t.Fatal(err)
				}
				neighbor := releasedFrom(t, s, bundle.ProjectID, rel.BundleID, rel.ExternalRef)
				boot := func(n int64) {
					t.Helper()
					if _, err := s.RequestReleasePreview(t.Context(), ReleaseSelection{ProjectID: bundle.ProjectID, Number: n}); err != nil {
						t.Fatal(err)
					}
				}
				boot(number)
				boot(neighbor)
				owner, err := s.projects.ByID(t.Context(), bundle.ProjectID)
				if err != nil || owner.ServingLabel != "p" {
					t.Fatalf("normal immutable serving label: %+v %v", owner, err)
				}
				name := docker.PreviewLabelName(docker.ReleasePreviewLabel(owner.ServingLabel, number))
				other := docker.PreviewLabelName(docker.ReleasePreviewLabel(owner.ServingLabel, neighbor))
				stack := docker.ReleaseStackID(owner.ServingLabel, number)
				otherStack := docker.ReleaseStackID(owner.ServingLabel, neighbor)
				if lookAt(t, s, name).IsZero() || lookAt(t, s, other).IsZero() {
					t.Fatal("actual boots left no view evidence")
				}
				if where == "worker" && (!remote.holds(name) || !remote.holds(other) || !s.servedOnWorker(name)) {
					t.Fatal("worker boot did not leave served resources")
				}
				if where == "local" && (!daemon.Running(name) || !daemon.Running(other)) {
					t.Fatal("local boots did not leave containers")
				}
				daemon.ResetCommands()
				// The boot uses recorded images; a retained disposable build-cache image
				// is additional owned cleanup evidence, not a claim that boot rebuilt it.
				image := docker.Current.BuildTag(name) + ":latest"
				failLocal := failure == "local"
				daemon.SetReply(func(args []string) ([]byte, error, bool) {
					line := strings.Join(args, " ")
					if strings.HasPrefix(line, "image ls") && strings.Contains(line, "reference="+docker.Current.BuildTag(name)) {
						return []byte(image + "\n"), nil, true
					}
					if failLocal && len(args) > 0 && args[0] == "rm" {
						return []byte("unavailable"), errors.New("local removal unavailable"), true
					}
					return nil, nil, false
				})
				if failure == "worker" {
					remote.err = errors.New("worker unavailable")
				}
				restore := func() {}
				if failure == "view" {
					restore = dbtest.NextStatementFailure(s.db, "-- name: ForgetPreviewView")
				}
				err = s.RemoveReleaseForGood(t.Context(), bundle.ProjectID, owner.ServingLabel, number)
				restore()
				if (err != nil) != (failure != "none") {
					t.Fatalf("retirement %s: %v", failure, err)
				}
				if len(daemon.Matching("image ls")) == 0 {
					t.Fatal("earlier failure suppressed independent local image retirement")
				}
				failLocal = false
				remote.err = nil
				if err := s.RemoveReleaseForGood(t.Context(), bundle.ProjectID, owner.ServingLabel, number); err != nil {
					t.Fatalf("retirement retry: %v", err)
				}
				if remote.holds(name) || daemon.Running(name) || s.servedOnWorker(name) || !lookAt(t, s, name).IsZero() {
					t.Fatal("owned release preview survived successful retirement")
				}
				if where == "worker" && (!remote.holds(other) || !s.servedOnWorker(other)) {
					t.Fatal("neighbor worker preview was retired")
				}
				if where == "local" && !daemon.Running(other) {
					t.Fatal("neighbor local preview was retired")
				}
				if lookAt(t, s, other).IsZero() {
					t.Fatal("neighbor view was forgotten")
				}
				if !slices.Contains(remote.images, name) || slices.Contains(remote.images, other) {
					t.Fatalf("worker image retirement: %v", remote.images)
				}
				commands := previewCommands(daemon)
				removedImage, removedStack := false, false
				for _, call := range commands {
					if strings.HasPrefix(call, "rmi ") && strings.Contains(call, image) {
						removedImage = true
						if strings.Contains(call, " -f ") {
							t.Fatal("forced image retirement")
						}
					}
					if strings.HasPrefix(call, "ps -aq") && strings.Contains(call, stack) {
						removedStack = true
					}
					if (strings.HasPrefix(call, "rm ") || strings.HasPrefix(call, "rmi ") || strings.HasPrefix(call, "volume rm")) && (strings.Contains(call, other) || strings.Contains(call, otherStack)) {
						t.Fatalf("retirement touched neighbor: %s", call)
					}
				}
				if !removedImage || !removedStack {
					t.Fatalf("exact image/stack retirement missing: %v", commands)
				}
				if err := s.RemoveReleaseForGood(t.Context(), bundle.ProjectID, owner.ServingLabel, number); err != nil {
					t.Fatalf("absent retry: %v", err)
				}
			})
		}
	}
}

func TestReleaseRetirementSerializesWithBootAndRefusesLaterLifecycle(t *testing.T) {
	s, remote, _, bundle, number, _ := releasePreviewWorld(t)
	actual := s.releaseHost
	entered, proceed := make(chan struct{}), make(chan struct{})
	s.releaseHost = releasePreviewHostFunc(func(ctx context.Context, selected Release) (ReleaseResult, error) {
		close(entered)
		select {
		case <-proceed:
		case <-ctx.Done():
			return ReleaseResult{}, ctx.Err()
		}
		return actual.BootReleasePreview(ctx, selected)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	bootDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := s.RequestReleasePreview(ctx, ReleaseSelection{ProjectID: bundle.ProjectID, Number: number})
		bootDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("boot did not acquire ownership")
	}
	retired := make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); retired <- s.RemoveReleaseForGood(ctx, bundle.ProjectID, "p", number) }()
	select {
	case err := <-retired:
		close(proceed)
		<-bootDone
		t.Fatalf("retirement passed active boot: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, bundle.ProjectID, access.Actor{Name: "owner"}, ids.Nil, false, time.Now())
	}); err != nil {
		close(proceed)
		<-bootDone
		t.Fatal(err)
	}
	close(proceed)
	if err := <-bootDone; err != nil {
		t.Fatalf("already-owned boot: %v", err)
	}
	if err := <-retired; err != nil {
		t.Fatalf("retirement after boot: %v", err)
	}
	name := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", number))
	if remote.holds(name) || s.servedOnWorker(name) || !lookAt(t, s, name).IsZero() {
		t.Fatal("late boot survived retirement")
	}
	// Once retirement has settled, a later request must refuse the deleting
	// lifecycle before invoking the boot adapter.
	s.releaseHost = releasePreviewHostFunc(func(context.Context, Release) (ReleaseResult, error) {
		return ReleaseResult{}, fmt.Errorf("boot effects reached deleting project")
	})
	if _, err := s.RequestReleasePreview(ctx, ReleaseSelection{ProjectID: bundle.ProjectID, Number: number}); err == nil || strings.Contains(err.Error(), "boot effects reached") {
		t.Fatalf("later lifecycle was not refused before boot: %v", err)
	}
}

func TestReleaseRetirementTransfersUnrevokedKeysWithoutLosingOwnership(t *testing.T) {
	s, w, projectID, provider := llmBootWorld(t, true)
	s.authority = releasePreviewAuthorityFunc(func(context.Context, ids.ID) error { return nil })
	repo := filepath.Join(w.dir, "workspaces", "project-"+projectID.String())
	llmWorkspace(t, repo, llmAppFixture)
	commit := gitCommitAll(t, repo, "release with AI models")
	bundle, err := s.bundles.Cut(t.Context(), projectID, commit, map[string]string{"web": "sha256:web"}, nil, []byte(llmAppFixture), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	number := releasedFrom(t, s, projectID, bundle, "sha256:web")
	neighbor := releasedFrom(t, s, projectID, bundle, "sha256:web")
	for _, n := range []int64{number, neighbor} {
		if _, err := s.RequestReleasePreview(t.Context(), ReleaseSelection{ProjectID: projectID, Number: n}); err != nil {
			t.Fatal(err)
		}
	}
	name := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", number))
	other := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", neighbor))
	held, err := s.previewKeys(projectID, name).Keys.Held(t.Context())
	if err != nil || len(held) != 1 || len(provider.Keys()) != 2 {
		t.Fatalf("actual boots must hold two distinct keys: %+v %v", held, err)
	}
	provider.FailNext("DELETE", 503)
	restore := dbtest.NextStatementFailure(s.db, "-- name: InsertRetainedResource")
	err = s.RemoveReleaseForGood(t.Context(), projectID, "p", number)
	restore()
	if err == nil {
		t.Fatal("failed revoke and failed durable transfer reported complete")
	}
	held, err = s.previewKeys(projectID, name).Keys.Held(t.Context())
	if err != nil || len(held) != 1 {
		t.Fatalf("failed transfer lost held ownership: %+v %v", held, err)
	}
	provider.FailNext("DELETE", 503)
	if err := s.RemoveReleaseForGood(t.Context(), projectID, "p", number); err != nil {
		t.Fatal(err)
	}
	rows, err := s.retained.ForProject(t.Context(), projectID)
	if err != nil || len(rows) != 1 || rows[0].Deployment != name || rows[0].Handle["hash"] == "" {
		t.Fatalf("durable retained key: %+v %v", rows, err)
	}
	held, err = s.previewKeys(projectID, name).Keys.Held(t.Context())
	if err != nil || len(held) != 0 {
		t.Fatalf("transferred key still held by preview: %+v %v", held, err)
	}
	held, err = s.previewKeys(projectID, other).Keys.Held(t.Context())
	if err != nil || len(held) != 1 || !w.client.Running(other) || len(provider.Keys()) != 2 {
		t.Fatalf("neighbor or unrevokeable key lost: %+v %v", held, err)
	}
}

// Runtime's injected clock pauses exactly at ownership acquisition, after
// release selection but before Begin returns. This distinguishes a lifecycle
// check before the lock from the required check after it.
func TestReleasePreviewRechecksLifecycleAfterOwnershipAcquisition(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed=%t", missing), func(t *testing.T) {
			s, remote, _, bundle, number, _ := releasePreviewWorld(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			name := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", number))
			// Establish real resources, then retire them before the delayed request.
			if _, err := s.RequestReleasePreview(ctx, ReleaseSelection{ProjectID: bundle.ProjectID, Number: number}); err != nil {
				t.Fatal(err)
			}
			if err := s.RemoveReleaseForGood(ctx, bundle.ProjectID, "p", number); err != nil {
				t.Fatal(err)
			}
			acquired, proceed := make(chan struct{}), make(chan struct{})
			var pause sync.Once
			s.runtime.now = func() time.Time {
				pause.Do(func() {
					close(acquired)
					select {
					case <-proceed:
					case <-ctx.Done():
					}
				})
				return time.Now()
			}
			done := make(chan error, 1)
			settled := make(chan struct{})
			go func() {
				defer close(settled)
				_, err := s.RequestReleasePreview(ctx, ReleaseSelection{ProjectID: bundle.ProjectID, Number: number})
				done <- err
			}()
			defer func() { cancel(); <-settled }()
			select {
			case <-acquired:
			case <-ctx.Done():
				t.Fatal("selected request did not reach ownership acquisition")
			}
			if err := s.db.InTx(ctx, func(tx *database.Tx) error {
				if !missing {
					return project.MarkDeletingIn(ctx, tx, bundle.ProjectID, access.Actor{Name: "owner"}, ids.Nil, false, time.Now())
				}
				if err := deployment.RemoveProjectIn(ctx, tx, bundle.ProjectID); err != nil {
					return err
				}
				return project.RemoveIn(ctx, tx, bundle.ProjectID, "", nil, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			close(proceed)
			if err := <-done; err == nil {
				t.Fatal("preselected request booted after lifecycle changed")
			}
			if remote.holds(name) || s.servedOnWorker(name) || !lookAt(t, s, name).IsZero() {
				t.Fatal("preselected request resurrected retired resources")
			}
		})
	}
}
