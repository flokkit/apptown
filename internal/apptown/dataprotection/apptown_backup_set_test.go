package dataprotection

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// A whole-AppTown backup's set of a built-in deployment is taken with every
// application part stopped (the need runs, for its copy) and the parts
// started again after: one moment of the app. It is kept on AppTown's disk,
// where the archive copies it from.
//
// Kills: a set copied while the app writes, parts left stopped after it, a
// set uploaded away from the disk the archive reads.
func TestAnAppTownBackupSetIsTakenWithTheAppStoppedAndStartsItAgain(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "abk")
	setCopies(client, func(req docker.Command) ([]byte, error) {
		_, err := io.WriteString(req.Stdout, "copy")
		return nil, err
	})
	dep, err := s.Deployments.ByID(t.Context(), dep.ID) // as it runs its release
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.TakeStoppedSet(context.Background(), dep, "the host's administrator")
	if err != nil {
		t.Fatal(err)
	}
	if b.Trigger != TriggerAppTownBackup || b.Status != BackupOK || b.Store != StoreAppTownDisk || !strings.HasPrefix(b.Path, filepath.Join(s.dataDir, "backups")) {
		t.Fatalf("the set: %+v", b)
	}
	cmds := commandsOf(client)
	dump := indexOf(cmds, "exec -e PGDATABASE -e PGHOST -e PGPASSWORD -e PGPORT -e PGSSLMODE -e PGUSER c-pg sh -c pg_dump")
	if dump < 0 {
		t.Fatalf("no copy of the database:\n%s", strings.Join(cmds, "\n"))
	}
	for _, c := range []string{"c-web", "c-worker"} {
		stop, start := indexOf(cmds, "stop "+c), indexOf(cmds, "start "+c)
		if stop < 0 || stop > dump || start < dump {
			t.Fatalf("%s: stopped at %d, started at %d, copied at %d:\n%s", c, stop, start, dump, strings.Join(cmds, "\n"))
		}
	}
	if indexOf(cmds, "stop c-pg") >= 0 {
		t.Fatal("the need was stopped before its copy")
	}
}

// With a backup store attached, the set a whole-AppTown backup takes still
// stays on AppTown's disk: the archive is written from there.
func TestAnAppTownBackupSetStaysOnDiskWithAStoreAttached(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "abs", 40110)
	w.attach()
	dep, err := w.s.Deployments.ByName(t.Context(), w.pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.s.TakeStoppedSet(t.Context(), dep, "the host's administrator")
	if err != nil {
		t.Fatal(err)
	}
	if b.Store != StoreAppTownDisk || !strings.HasPrefix(b.Path, filepath.Join(w.dir, "backups")) {
		t.Fatalf("the set went to the store: %+v", b)
	}
}

// A set kept on disk, restored with AppTown's data directory under another
// path, is found there once its record is moved: it restores, and the daily
// move to the store takes it from its new place.
//
// Kills: records left naming the old directory, where nothing is.
func TestDiskSetsAreFoundWhereTheRestoredDataDirectoryIs(t *testing.T) {
	t.Parallel()
	w := newOffhostWorld(t, "rel", 40111)
	ctx := t.Context()
	taken := w.take("scheduled")
	rel, err := filepath.Rel(w.dir, taken.Path)
	if err != nil {
		t.Fatal(err)
	}
	// The same set as the source recorded it: under its own data directory.
	old := "/srv/source/data"
	moved := *taken
	moved.ID, moved.Path = ids.Nil, filepath.Join(old, rel)
	id, err := w.s.backups.Record(ctx, &moved, "the source")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.s.RelocateDiskSets(ctx, old); err != nil || n != 1 {
		t.Fatalf("relocated %d: %v", n, err)
	}
	if n, err := w.s.RelocateDiskSets(ctx, w.dir); err != nil || n != 0 {
		t.Fatalf("the same directory moved %d: %v", n, err)
	}
	got, err := w.s.backups.ByID(ctx, w.pid, "main", id)
	if err != nil || got.Path != taken.Path {
		t.Fatalf("the moved record: %+v %v", got, err)
	}
	if res, err := w.s.restoreBackup(ctx, nil, w.pid, "main", id, "alice"); err != nil || res.Status != RestoreNotVerified {
		t.Fatalf("restoring the moved set: %+v %v", res, err)
	}
}

// A whole-AppTown backup's set of a deployment whose release cannot be read
// is not planned from its list alone: what it would hold is not known, so
// it is ErrDataNotKnown (the archive says that deployment's data is not
// whole in it), and nothing is stopped.
//
// Kills: a set planned without the release's declared data, read as whole.
func TestAnAppTownBackupSetOfAnUnreadableReleaseIsNotKnown(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "abu")
	unread := *dep
	unread.ReleaseSHA = "" // a release AppTown cannot read: none is recorded
	if b, err := s.TakeStoppedSet(context.Background(), &unread, "the host's administrator"); !errors.Is(err, ErrDataNotKnown) || b != nil {
		t.Fatalf("a set of an unreadable release: %+v %v", b, err)
	}
	for _, c := range commandsOf(client) {
		if strings.HasPrefix(c, "stop ") {
			t.Fatalf("a part was stopped for a set that was not taken: %s", c)
		}
	}
}
