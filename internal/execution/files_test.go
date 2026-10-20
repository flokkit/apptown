package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/execfile"
)

// recordingDocker is a docker executable that records its arguments, and,
// for a run, copies each file mounted read-only at /etc/... into the
// recording, as the container would read it.
func recordingDocker(t *testing.T) (binary, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	binary = filepath.Join(dir, "docker")
	// A run's own inspection says it was not killed. The disk read answers
	// what dir/df holds, fails while dir/unread exists, and otherwise
	// answers a disk with room; a task inspected is
	// one already created while dir/exists exists, cannot be asked about
	// while dir/offline exists, and is absent otherwise.
	script := `#!/bin/sh
[ "$1" = inspect ] && [ "$2" != --type ] && { echo false; exit 0; }
[ "$1" = inspect ] && { [ -f "` + dir + `/exists" ] && { echo "c1 created 0 false"; exit 0; }; [ -f "` + dir + `/offline" ] && exit 1; echo "Error: No such object: $4"; exit 1; }
[ "$1" = info ] && { echo /var/lib/docker; exit 0; }
case "$*" in *" df -Pk /d")
  [ -f "` + dir + `/unread" ] && exit 125
  [ -f "` + dir + `/df" ] && { cat "` + dir + `/df"; exit 0; }
  printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 104857600 10485760 94371840 10%% /d\n'; exit 0;;
esac
echo "$@" >> "` + calls + `"
prev=""
for arg in "$@"; do
  if [ "$prev" = "-v" ]; then
    case "$arg" in *:ro) src="${arg%%:*}"; dst="${arg#*:}"; dst="${dst%:ro}"; echo "file $dst: $(cat "$src")" >> "` + calls + `";; esac
  fi
  prev="$arg"
done
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, calls
}

// A new local task is refused, with nothing created, while its Docker's
// data folder has less than its floor free or cannot be read; the same
// key ensured again after its container was made finds that container.
//
// Kills: admitDisk removed from Ensure; an unread disk admitted; a retried
// ensure of a created task refused; an unobservable task refused (DK-C01);
// the task's files staged before the refusal (DK-C04); a key whose files an
// earlier ensure staged refused with them left (DK-C06).
func TestALocalTaskIsRefusedOnADiskShortOfSpace(t *testing.T) {
	t.Parallel()
	binary, calls := recordingDocker(t)
	dir := filepath.Dir(calls)
	df, unread, exists := filepath.Join(dir, "df"), filepath.Join(dir, "unread"), filepath.Join(dir, "exists")
	reading := func(totalKiB, freeKiB int64) {
		t.Helper()
		body := fmt.Sprintf("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x %d %d %d 99%% /d\n", totalKiB, totalKiB-freeKiB, freeKiB)
		if err := os.WriteFile(df, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := DockerTasks{Binary: binary, Namespace: "test", Files: t.TempDir()}
	spec := TaskSpec{Key: "script-1", Image: "alpine:3", Argv: []string{"true"}, Admission: Admission{Profile: "local", Network: NetworkDefault},
		Files: []File{configFile}}
	created := func() bool { b, _ := os.ReadFile(calls); return strings.Contains(string(b), "create") }

	reading(100<<20, 5<<20-1) // 100 GiB, just under 5 GiB free
	if _, err := d.Ensure(context.Background(), spec); !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "/var/lib/docker") {
		t.Fatalf("a task on a disk short of space: %v", err)
	}
	if err := os.WriteFile(unread, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Ensure(context.Background(), spec); !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("a task on an unread disk: %v", err)
	}
	if err := os.Remove(unread); err != nil {
		t.Fatal(err)
	}
	if created() {
		t.Fatal("a refused task was created")
	}
	if _, err := os.Stat(d.filesPath(spec.Key)); !os.IsNotExist(err) {
		t.Fatalf("a refused task left its generated files: %v", err)
	}
	// A key whose files an earlier ensure staged (its create got no answer
	// and made no container) was admitted then: it is ensured again, not
	// refused with its files left behind.
	if _, err := StageFiles(d.filesPath("script-earlier"), spec.Files); err != nil {
		t.Fatal(err)
	}
	earlier := spec
	earlier.Key = "script-earlier"
	if _, err := d.Ensure(context.Background(), earlier); err != nil || !created() {
		t.Fatalf("a key an earlier ensure staged, on a short disk: %v", err)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	// A caller that admitted the disk itself (a worker) is not asked again.
	if _, err := (DockerTasks{Binary: binary, Namespace: "test", Files: t.TempDir(), CallerAdmitsDisk: true}).Ensure(context.Background(), spec); err != nil || !created() {
		t.Fatalf("a task its caller admitted: %v", err)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	reading(100<<20, 5<<20)
	if _, err := d.Ensure(context.Background(), spec); err != nil || !created() {
		t.Fatalf("a task with its floor free: %v", err)
	}
	// With the disk short and the task not observable, whether it was made
	// is unknown: never a refusal with nothing made.
	reading(100<<20, 1)
	offline := filepath.Join(dir, "offline")
	if err := os.WriteFile(offline, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bare := spec // nothing staged for it: only its container says it exists
	bare.Key, bare.Files = "script-bare", nil
	if _, err := d.Ensure(context.Background(), bare); errors.Is(err, diskspace.ErrRefused) || !errors.Is(err, ErrTaskUnobservable) {
		t.Fatalf("a task that could not be observed on a short disk: %v", err)
	}
	if err := os.Remove(offline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exists, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Ensure(context.Background(), bare); errors.Is(err, diskspace.ErrRefused) {
		t.Fatalf("an ensure repeated after its container was made was refused: %v", err)
	}
}

var configFile = File{Path: "/etc/mise/config.toml", Content: []byte("[settings]\n"), Mode: 0o644}

// A Docker run is given its generated files read-only at their paths,
// written beside its tree in the runner's own folder, which goes after the
// run; nothing is written into the tree.
func TestGeneratedFilesArePresentedBesideTheTree(t *testing.T) {
	t.Parallel()
	binary, calls := recordingDocker(t)
	scratch, tree := t.TempDir(), t.TempDir()
	out := (Runner{DockerBinary: binary, Scratch: scratch}).Run(context.Background(), Docker, Request{
		Workspace: tree, Argv: []string{"true"}, Image: "alpine:3", Name: "forge-test", Timeout: time.Minute,
		Files: []File{configFile}})
	if !out.Exited0() {
		t.Fatalf("%+v", out)
	}
	recorded, _ := os.ReadFile(calls)
	if !strings.Contains(string(recorded), "file /etc/mise/config.toml: [settings]") {
		t.Fatalf("the container was not given its file:\n%s", recorded)
	}
	if entries, _ := os.ReadDir(scratch); len(entries) != 0 {
		t.Fatalf("the generated files outlived the run: %v", entries)
	}
	if entries, _ := os.ReadDir(tree); len(entries) != 0 {
		t.Fatalf("the run's tree was written: %v", entries)
	}
	// A process on this machine is given no generated files.
	if out := (Runner{}).Run(context.Background(), Local, Request{Argv: []string{"true"}, Timeout: time.Minute, Files: []File{configFile}}); out.Termination != LaunchFailed {
		t.Fatalf("a local process was given generated files: %+v", out)
	}
}

// Files that cannot be written stop the run before Docker is asked
// anything.
func TestFilesThatCannotBeWrittenRunNothing(t *testing.T) {
	t.Parallel()
	binary, calls := recordingDocker(t)
	notAFolder := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notAFolder, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]struct {
		runner Runner
		dir    string
	}{
		"no folder for them":          {Runner{DockerBinary: binary, Scratch: notAFolder}, ""},
		"the files cannot be written": {Runner{DockerBinary: binary}, filepath.Join(notAFolder, "files")},
	} {
		out := r.runner.Run(context.Background(), Docker, Request{
			Argv: []string{"true"}, Image: "alpine:3", Name: "forge-test", Timeout: time.Minute, Files: []File{configFile}, FilesDir: r.dir})
		if out.Termination != LaunchFailed || out.RunErr == nil {
			t.Fatalf("%s: %+v", name, out)
		}
		if _, err := os.Stat(calls); !os.IsNotExist(err) {
			t.Fatalf("%s: Docker was asked to run although the files could not be written", name)
		}
	}
}

// A generated file is placed only at an absolute, clean path of its own,
// outside the tree and the history, with permission bits, within bounds.
func TestGeneratedFilesArePlacedOnlyOutsideTheTree(t *testing.T) {
	t.Parallel()
	for name, files := range map[string][]File{
		"in the tree":    {{Path: "/work/.npmrc", Content: []byte("x"), Mode: 0o644}},
		"the tree":       {{Path: "/work", Content: []byte("x"), Mode: 0o644}},
		"in the history": {{Path: "/history/x", Content: []byte("x"), Mode: 0o644}},
		"relative":       {{Path: "etc/x", Content: []byte("x"), Mode: 0o644}},
		"not clean":      {{Path: "/etc/../work/x", Content: []byte("x"), Mode: 0o644}},
		"a mount option": {{Path: "/etc/x:rw", Content: []byte("x"), Mode: 0o644}},
		"twice":          {configFile, configFile},
		"no mode":        {{Path: "/etc/x", Content: []byte("x")}},
		"not a file":     {{Path: "/etc/x", Content: []byte("x"), Mode: 0o644 | os.ModeSymlink}},
		"too large":      {{Path: "/etc/x", Content: make([]byte, MaxFileBytes+1), Mode: 0o644}},
	} {
		if err := ValidFiles(files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidFiles([]File{configFile, {Path: "/apptown-cache/cargo/config.toml", Content: []byte("x"), Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
}

// A task's files, once written, are used again only if they are exactly the
// same: other bytes or another mode at the same paths are refused, never
// written over files a container may have mounted.
func TestTaskFilesAreNeverRewritten(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "task")
	first, err := StageFiles(dir, []File{configFile})
	if err != nil {
		t.Fatal(err)
	}
	again, err := StageFiles(dir, []File{configFile})
	if err != nil || strings.Join(again, " ") != strings.Join(first, " ") {
		t.Fatalf("the same files again: %v %v", again, err)
	}
	for name, changed := range map[string]File{
		"other bytes":  {Path: configFile.Path, Content: []byte("[other]\n"), Mode: 0o644},
		"another mode": {Path: configFile.Path, Content: configFile.Content, Mode: 0o600},
	} {
		if _, err := StageFiles(dir, []File{changed}); !errors.Is(err, ErrTaskConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if body, _ := os.ReadFile(strings.SplitN(first[0], ":", 2)[0]); string(body) != "[settings]\n" {
		t.Fatalf("a staged file was rewritten: %q", body)
	}
}

// A local task's files are written into the task's own folder before it is
// created, kept while it exists, and removed when it is released; a driver
// with no folder for them refuses the task.
func TestALocalTaskKeepsItsFilesUntilItIsReleased(t *testing.T) {
	t.Parallel()
	binary, calls := recordingDocker(t)
	root := t.TempDir()
	d := DockerTasks{Binary: binary, Files: root, Namespace: "test"}
	spec := TaskSpec{Key: "script-1", Image: "alpine:3", Argv: []string{"true"}, Admission: Admission{Profile: "local", Network: NetworkDefault},
		Files: []File{configFile}}
	if _, err := d.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	recorded, _ := os.ReadFile(calls)
	if !strings.Contains(string(recorded), "create") || !strings.Contains(string(recorded), "file /etc/mise/config.toml: [settings]") {
		t.Fatalf("the task was not created with its file:\n%s", recorded)
	}
	folder := d.filesPath(spec.Key)
	if _, err := os.Stat(folder); err != nil {
		t.Fatal("the task's files are not kept")
	}
	changed := spec
	changed.Files = []File{{Path: configFile.Path, Content: []byte("[other]\n"), Mode: 0o644}}
	if _, err := d.Ensure(context.Background(), changed); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("an ensure with other files: %v", err)
	}
	if err := d.Release(context.Background(), TaskRef{Key: spec.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatal("the task's files outlived it")
	}
	if _, err := (DockerTasks{Binary: binary}).Ensure(context.Background(), spec); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a driver with no folder for files: %v", err)
	}
}
