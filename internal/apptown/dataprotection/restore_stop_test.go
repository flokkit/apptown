package dataprotection

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A restore puts data back only while nothing can write it: every
// application part of the deployment is stopped first, and a part that will
// not stop refuses the restore before anything is written. Afterwards every
// stopped part is started again, and one that will not start is named.
//
// Kills: a restore that stopped only the public container, went on writing
// when that stop failed, and read as a success into a running app.
func TestARestoreWritesNothingUnlessEveryPartIsStopped(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "rq", answeringPort(t))
	// The running release has a second part, a worker, which the
	// deployment's list does not record (a list made before recording);
	// the list records a third container the release does not name.
	releaseMain(t, s, "rq", "app:\n  web:\n    image: registry.example/rq:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n  worker:\n    image: registry.example/rq:1\n    command: [\"work\"]\nneeds:\n  postgres: {}\n")
	app := docker.Current.Container("rq", "main")
	worker := docker.Current.Service(docker.StackID("rq", "main"), "worker")
	listed := "apptown-svc-env-rq-main-cron"
	for _, c := range []string{app, worker, listed} {
		client.SetRunning(c, true)
	}
	dep, err := s.Deployments.ByName(context.Background(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.OwnerOf(dep).Created(context.Background(), deploy.Owned{Kind: "container", ID: listed, Name: listed}); err != nil {
		t.Fatal(err)
	}
	var lifecycle, writes []string
	failStop, failStart := "", ""
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		if req.Stdin != nil {
			writes = append(writes, strings.Join(req.Args, " "))
			_, err := io.ReadAll(req.Stdin)
			return nil, err
		}
		line := strings.Join(req.Args, " ")
		lifecycle = append(lifecycle, line)
		if line == "stop "+failStop || line == "start "+failStart {
			return []byte("Error response from daemon: refused"), errors.New("exit status 1")
		}
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil {
		t.Fatal(err)
	}

	// The worker will not stop: nothing is written and the app, already
	// stopped, is started again.
	failStop, lifecycle = worker, nil
	if _, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls"); err == nil || !strings.Contains(err.Error(), "nothing was restored") {
		t.Fatalf("a part that will not stop refuses the restore: %v", err)
	}
	lc := strings.Join(lifecycle, "\n")
	if len(writes) != 0 || !strings.Contains(lc, "stop "+app) || !strings.Contains(lc, "start "+app) {
		t.Fatalf("nothing is written and the stopped app comes back: writes %v\n%s", writes, lc)
	}

	// Both stop, the data goes back, both start. (The fake daemon marks a
	// container stopped though its stop was refused; Docker leaves it
	// running, and so it is here.)
	client.SetRunning(worker, true)
	failStop, lifecycle = "", nil
	res, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	lc = strings.Join(lifecycle, "\n")
	for _, want := range []string{"stop " + app, "stop " + worker, "stop " + listed, "start " + app, "start " + worker, "start " + listed} {
		if !strings.Contains(lc, want) {
			t.Fatalf("every part is stopped and started: missing %q in\n%s", want, lc)
		}
	}
	if len(writes) == 0 || !res.Stopped || res.Started == nil || !*res.Started || len(res.NotStarted) != 0 {
		t.Fatalf("a restore with every part back: %+v", res)
	}

	// The worker will not start again: the data is back and says so, and
	// the result names the part that is not running.
	failStart = worker
	res, err = s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Started == nil || *res.Started || len(res.NotStarted) != 1 || !strings.Contains(res.NotStarted[0], worker) || res.Status != RestoreNotVerified {
		t.Fatalf("a part that would not start is named, the data's status stands: %+v", res)
	}
}

// A part the running release names that Docker does not have cannot write,
// so the data still goes back; but the app does not come back without it,
// and the result says so instead of reading as recovered.
//
// Kills: a restore that skipped an absent part silently and reported every
// part started (or, with nothing stopped, nothing about the app at all).
func TestARestoreNamesAPartThatIsNotThere(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "rm", 40003)
	releaseMain(t, s, "rm", "app:\n  web:\n    image: registry.example/rm:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n  worker:\n    image: registry.example/rm:1\n    command: [\"work\"]\nneeds:\n  postgres: {}\n")
	app := docker.Current.Container("rm", "main")
	worker := docker.Current.Service(docker.StackID("rm", "main"), "worker")
	client.SetRunning(app, true) // the worker's container is gone
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		if req.Stdin != nil {
			_, err := io.ReadAll(req.Stdin)
			return nil, err
		}
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Status != RestoreNotVerified || res.Started == nil || *res.Started || res.Ready != nil ||
		len(res.NotStarted) != 1 || !strings.Contains(res.NotStarted[0], worker) || !strings.Contains(res.NotStarted[0], "not there") {
		t.Fatalf("the data is back and the missing worker is named: %+v", res)
	}
}

// A restore stops only the parts that run, and starts again exactly those:
// a release's worker someone stopped, and a listed container someone
// stopped, stay stopped and are not reported missing; the app, stopped and
// started again, is asked whether it is ready, and is.
//
// Kills: a restore that starts a worker a person stopped, or reruns a
// completed one-off container; one that reads a stopped part as missing
// and the app as not started.
func TestARestoreStartsAgainOnlyWhatItStopped(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "rr", answeringPort(t))
	releaseMain(t, s, "rr", "app:\n  web:\n    image: registry.example/rr:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n  worker:\n    image: registry.example/rr:1\n    command: [\"work\"]\nneeds:\n  postgres: {}\n")
	app := docker.Current.Container("rr", "main")
	worker := docker.Current.Service(docker.StackID("rr", "main"), "worker")
	listed := "apptown-svc-env-rr-main-cron"
	for _, c := range []string{app, worker, listed} {
		client.SetRunning(c, true)
	}
	for _, c := range []string{worker, listed} {
		if _, err := client.Run(context.Background(), "", docker.Command{Args: []string{"stop", c}}); err != nil {
			t.Fatal(err)
		}
	}
	dep, err := s.Deployments.ByName(context.Background(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.OwnerOf(dep).Created(context.Background(), deploy.Owned{Kind: "container", ID: listed, Name: listed}); err != nil {
		t.Fatal(err)
	}
	var lifecycle []string
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		if req.Stdin != nil {
			_, err := io.ReadAll(req.Stdin)
			return nil, err
		}
		lifecycle = append(lifecycle, strings.Join(req.Args, " "))
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.restoreBackup(context.Background(), nil, pid, "main", b.ID, "bls")
	if err != nil {
		t.Fatal(err)
	}
	lc := strings.Join(lifecycle, "\n")
	if !strings.Contains(lc, "stop "+app) || !strings.Contains(lc, "start "+app) {
		t.Fatalf("the app is stopped and started:\n%s", lc)
	}
	for _, c := range []string{worker, listed} {
		if strings.Contains(lc, "start "+c) || strings.Contains(lc, "stop "+c) {
			t.Fatalf("%s, stopped by a person, was touched:\n%s", c, lc)
		}
	}
	if !res.Stopped || res.Started == nil || !*res.Started || len(res.NotStarted) != 0 || res.Ready == nil || !*res.Ready {
		t.Fatalf("the restore's account: stopped %v started %v not started %v ready %v", res.Stopped, res.Started, res.NotStarted, res.Ready)
	}
}

// A part the running release names that is not there cannot be started,
// so a restore that ran through the operation fails and names it, though
// the data is back (code review R1).
//
// Kills: a restore operation that succeeds while the app is not whole.
func TestARestoreOperationFailsAndNamesAPartThatIsNotThere(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid := backupWorld(t, "rm", 40003)
	releaseMain(t, s, "rm", "app:\n  web:\n    image: registry.example/rm:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n  worker:\n    image: registry.example/rm:1\n    command: [\"work\"]\nneeds:\n  postgres: {}\n")
	app := docker.Current.Container("rm", "main")
	worker := docker.Current.Service(docker.StackID("rm", "main"), "worker")
	client.SetRunning(app, true) // the worker's container is gone
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		if req.Stdout != nil {
			_, err := io.WriteString(req.Stdout, "copy of "+filepath.Base(req.Stdout.(*os.File).Name()))
			return nil, err
		}
		if req.Stdin != nil {
			_, err := io.ReadAll(req.Stdin)
			return nil, err
		}
		return nil, nil
	})
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil {
		t.Fatal(err)
	}
	op, res := restoreThroughOperation(t, s, pid, "main", b.ID)
	if op.State.String() != "failed" || !strings.Contains(op.Detail, worker) {
		t.Errorf("a missing part fails the restore, named: %s %q", op.State, op.Detail)
	}
	if res.Status != RestoreNotVerified || res.Started == nil || *res.Started || res.Ready != nil ||
		len(res.NotStarted) != 1 || !strings.Contains(res.NotStarted[0], worker) || !strings.Contains(res.NotStarted[0], "not there") {
		t.Fatalf("the data is back and the missing worker is named: %+v", res)
	}
}
