package dataprotection

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// copyingDaemon answers every copy command with "copy of <file>".
func copyingDaemon() *dockerfake.Daemon {
	d := dockerfake.New()
	setBackupDockerCommand(d, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		return nil, nil
	})
	return d
}

// A Docker host's final set in the store that holds Postgres's portable
// copy beside something a new managed database cannot be filled from (the
// app's files, or Redis's copy with no portable format) is not offered to
// start from, and choosing it is refused before anything is made, naming
// what would be left behind (OH-01).
//
// Kills: a managed start that fills the portable item and silently skips
// the rest; offering such a set as a final copy to start from.
func TestAMixedDockerSetIsRefusedByAManagedStart(t *testing.T) {
	for _, other := range []string{"files", "redis"} {
		t.Run(other, func(t *testing.T) {
			t.Parallel()
			w := newDataWorld(t)
			w.protect()
			ctx := context.Background()
			pg, err := w.s.catalog.ByCapability(ctx, "postgres")
			if err != nil {
				t.Fatal(err)
			}
			plan := []backupPlanItem{needItem(pg, "postgres", "c-pg")}
			want := "the app's files"
			if other == "files" {
				plan = append(plan, backupPlanItem{Kind: "volume", Name: "apptown-data-apptown-env-ledger-old", File: "data.tar.gz", How: "tar of /data", Copy: CopyFiles,
					Step: docker.Step{Image: "test-helper", Argv: []string{"tar", "cz", "-C", "/src", "."}}})
			} else {
				redis, err := w.s.catalog.ByCapability(ctx, "redis")
				if err != nil {
					t.Fatal(err)
				}
				plan, want = append(plan, needItem(redis, "redis", "c-redis")), "redis (a copy with no portable format)"
			}
			set, err := w.s.takeSet(ctx, docker.Scope{Client: copyingDaemon()}, w.project, "ledger", "old", plan, TriggerTakeDown, "alice")
			if err != nil || set.Status != BackupOK || set.Store != StoreBackupStore || set.GoodCopy("need", "postgres").Format != "pg-custom" {
				t.Fatalf("the Docker final set: %+v %v", set, err)
			}
			finals, err := w.s.FinalCopies(ctx, w.project)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range finals {
				if f.Backup == set.ID {
					t.Fatalf("a set a managed start would refuse is offered: %+v", finals)
				}
			}
			if _, err := w.bind(w.project, "prod", "production", set.ID); !errors.Is(err, deployment.ErrStartFrom) || !strings.Contains(err.Error(), want) {
				t.Fatalf("starting from a mixed set: %v; want a refusal naming %q", err, want)
			}
			if _, err := w.s.Deployments.ByName(ctx, w.project, "prod"); err == nil {
				t.Fatal("a refused start recorded a deployment")
			}
		})
	}
}

// filesContract is a release that keeps its data in a folder and needs no
// database.
const filesContract = `app:
  web:
    build: .
    ports: ["8080"]
    public: true
    dataDir: /data
    health: { readiness: { http: /healthz } }
`

// A store set holding only the app's files rehearses, chosen by its id or
// as the newest: the files are unpacked into the rehearsal's new data
// volume, the one its app mounts at its dataDir, before the app starts.
// A release with no dataDir cannot take them, and says so (OH-02).
//
// Kills: rehearsal admission that requires a portable database copy;
// booting the app before its files are in; leaving the files out silently.
func TestASetOfOnlyTheAppsFilesRehearses(t *testing.T) {
	t.Parallel()
	w := newRehearsalWorld(t)
	ctx := context.Background()
	set, err := w.s.takeSet(ctx, docker.Scope{Client: copyingDaemon()}, w.project, "ledger", "prod",
		[]backupPlanItem{{Kind: "volume", Name: "apptown-data-apptown-env-ledger-prod", File: "data.tar.gz", How: "tar of /data", Copy: CopyLiveFiles, Step: docker.Step{Image: "test-helper", Argv: []string{"tar", "cz", "-C", "/src", "."}}}},
		"scheduled", "alice")
	if err != nil || set.Status != BackupOK || set.Store != StoreBackupStore {
		t.Fatalf("the files-only set: %+v %v", set, err)
	}
	// The release it serves now declares no dataDir: the files have
	// nowhere to go.
	op, err := w.s.RequestRehearsal(ctx, w.project, "prod", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.run(op); got.State != operations.Failed || !strings.Contains(got.Detail, "declares no dataDir") {
		t.Fatalf("a release with no dataDir: %s %s", got.State, got.Detail)
	}

	// The release it serves keeps its data in a folder.
	repo := filepath.Join(w.s.dataDir, "workspaces", "project-ledger")
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(filesContract), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "files"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	revision, err := releaseGit(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	bid, err := w.s.Bundles.Cut(ctx, w.project, strings.TrimSpace(revision), map[string]string{"web": releaseRepo + digestA}, nil, []byte(filesContract),
		digestOrigin(&claimedAttempt{db: w.db, project: w.project, run: idstest.N(8), step: idstest.N(80), at: 1}))
	if err != nil {
		t.Fatal(err)
	}
	number, err := w.s.Releases.LatestReleasedNumber(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := w.s.Releases.ByNumber(ctx, w.project, number)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: as newRehearsalWorld, a digest bundle's App Platform release copies images between registries this world has none of; the rehearsal reads only which bundle the served release names.
	if _, err := w.db.Exec(`UPDATE releases SET bundle_id=$1 WHERE id=$2`, bid, rel.ID); err != nil {
		t.Fatal(err)
	}
	for _, chosen := range []ids.ID{set.ID, ids.Nil} {
		op, err := w.s.RequestRehearsal(ctx, w.project, "prod", chosen)
		if err != nil {
			t.Fatalf("set %d: %v", chosen, err)
		}
		got := w.run(op)
		if !endedAs(got, OperationVerified) {
			t.Fatalf("rehearsing set %d: %s %s", chosen, got.State, got.Detail)
		}
		place := placeOf(op.ID, "rehearse-1")
		data := docker.Current.DataVolume(place.App)
		cmds := w.host.Commands()
		unpack := slices.IndexFunc(cmds, func(c []string) bool {
			line := strings.Join(c, " ")
			return strings.Contains(line, "-v "+place.Copy+":"+copyMount+":ro -v "+data+":/dst") && strings.HasSuffix(line, "tar xz -C /dst < "+copyMount+"/data.tar.gz")
		})
		app := slices.IndexFunc(cmds, func(c []string) bool {
			return c[0] == "run" && slices.Contains(c, "--name") && c[slices.Index(c, "--name")+1] == place.App
		})
		if unpack < 0 || app < 0 || unpack > app || !slices.Contains(cmds[app], data+":/data") {
			t.Fatalf("set %d: the files unpacked at %d, the app started at %d with %v", chosen, unpack, app, cmds[max(app, 0)])
		}
		if staged := w.host.volumes[place.Copy+"/data.tar.gz"]; staged != "copy of data.tar.gz" {
			t.Fatalf("set %d: staged %q", chosen, staged)
		}
	}

	// A token revoked once the copy is staged unpacks nothing into the new
	// data volume: the rehearsal asks again before it fills the app's files.
	op, err = w.s.RequestRehearsal(ctx, w.project, "prod", set.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.host.mu.Lock()
	w.host.afterStage = func() { w.auth.revoke("token:alice") }
	w.host.mu.Unlock()
	withdrawn(t, w.run(op))
	for _, c := range w.host.Commands() {
		if line := strings.Join(c, " "); strings.Contains(line, docker.Current.DataVolume(placeOf(op.ID, "rehearse-1").App)+":/dst") {
			t.Fatalf("the app's files were unpacked after authority was withdrawn: %s", line)
		}
	}
}
