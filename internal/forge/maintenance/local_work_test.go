package maintenance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/tasks"
)

// The records name a chat placed on the forge that has not ended and a
// workflow cleanup job not completed, each as local work; a chat on a
// worker and a job of another kind or key are not. Records holding
// nothing local leave nothing. Tasks and running steps are proved with the
// drain in the root (no_local_docker_test.go), where steps are admitted.
func TestLocalWorkLeftNamesUnendedLocalChatsAndOutstandingCleanups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := databaseSuite.New(t)
	if err := jobs.Migrate(ctx, db.Pool()); err != nil {
		t.Fatal(err)
	}
	sessions := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	t.Cleanup(func() { _ = sessions.Shutdown(context.Background()) })
	left := func() error {
		return LocalWorkLeft(ctx, db, tasks.New(db), sessions, project.New(db, nil), "workflow-abandonment", "workflow-completion")
	}
	if err := left(); err != nil {
		t.Fatalf("records with no work: %v", err)
	}
	open := func(labels map[string]string) string {
		t.Helper()
		s, err := sessions.Open(ctx, console.OpenRequest{Scope: "change:1", Labels: labels, Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls"})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	open(map[string]string{conversation.PlacementLabel: conversation.OnWorker})
	if err := left(); err != nil {
		t.Fatalf("a chat on the worker only: %v", err)
	}
	local := open(nil)
	if err := left(); err == nil || !strings.Contains(err.Error(), "chat "+local+" on AppTown, not ended") {
		t.Fatalf("an unended local chat: %v", err)
	}
	if _, err := sessions.End(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := left(); err != nil {
		t.Fatalf("once the local chat ended: %v", err)
	}
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		for kind, key := range map[string]string{"workflow-completion": "1/cleanup", "workflow-abandonment": "2/stop", "other": "3/cleanup"} {
			if _, err := jobs.InsertOnTx(ctx, tx.Native(), "", jobs.Request{Operation: kind, Key: key}, 3, time.Time{}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := left()
	if err == nil || !strings.Contains(err.Error(), "workflow-completion 1/cleanup, not completed") {
		t.Fatalf("an outstanding workflow cleanup: %v", err)
	}
	for _, not := range []string{"2/stop", "3/cleanup"} {
		if strings.Contains(err.Error(), not) {
			t.Fatalf("named %s, which is no workflow cleanup: %v", not, err)
		}
	}
}

// The drain's Docker question: every container carrying one of the forge's
// execution labels counts, stopped or not, whoever's installation it is;
// a deployment's release container (env), the built-in database (infra) and
// a container with no forge label do not. A Docker that cannot be asked is
// an error, never an empty answer.
func TestExecutionContainersAreThoseWithAnExecutionLabel(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "ps" {
			return []byte(strings.Join([]string{
				"forge-step-00000000-0007-7000-8000-000000000000-agent\tstep\t\t7\t/data/runs/1",
				"forge-task-ns-op\t\top\t\t",
				"forge-script\t\t\t7\t/data/runs/1",
				"forge-writer\t\t\t\t/data/workspaces/change-00000000-0001-7000-8000-000000000000",
				"forge-env-game-main\tenv\t\t\t",
				"forge-db\tinfra\t\t\t",
				"someone-elses\t\t\t\t",
				"bare",
				"",
			}, "\n")), nil, true
		}
		return nil, nil, false
	})
	got, err := ExecutionContainers(context.Background(), docker.Scope{Client: daemon})
	want := []string{"forge-step-00000000-0007-7000-8000-000000000000-agent (step)", "forge-task-ns-op", "forge-script", "forge-writer"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("execution containers: %q %v, want %q", got, err, want)
	}

	down := dockerfake.New()
	down.SetReply(func([]string) ([]byte, error, bool) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1"), true
	})
	if got, err := ExecutionContainers(context.Background(), docker.Scope{Client: down}); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("a Docker that cannot be asked: %q %v", got, err)
	}
}

// A preview's build images are removed without force: one a container
// still uses stays and is no failure; a removal that fails otherwise, and
// a listing that fails, are named.
func TestRemoveBuildImagesNeverForcesAndNamesFailures(t *testing.T) {
	t.Parallel()
	const own, part, inUse, broken = "forge-preview-build-forge-preview-run-00000000-0001-7000-8000-000000000000:latest", "forge-preview-build-forge-preview-run-00000000-0001-7000-8000-000000000000-web:latest", "forge-preview-build-forge-preview-run-00000000-0001-7000-8000-000000000000-db:latest", "forge-preview-build-forge-preview-run-00000000-0001-7000-8000-000000000000-x:latest"
	daemon := dockerfake.New()
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
	failed := RemoveBuildImages(context.Background(), docker.Scope{Client: daemon}, "forge-preview-run-00000000-0001-7000-8000-000000000000")
	if len(failed) != 1 || !strings.Contains(failed[0], broken) {
		t.Fatalf("failures: %q", failed)
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

	unlisted := dockerfake.New()
	unlisted.SetReply(func([]string) ([]byte, error, bool) { return []byte("daemon gone"), errors.New("exit status 1"), true })
	if failed := RemoveBuildImages(context.Background(), docker.Scope{Client: unlisted}, "forge-preview-run-00000000-0001-7000-8000-000000000000"); len(failed) != 1 {
		t.Fatalf("a listing that failed: %q", failed)
	}
}
