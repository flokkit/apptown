//go:build unix

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/poll/polltest"
)

// A run's services are a precondition, never a guess: a service that does
// not become ready means the run never starts (LaunchFailed, not a failed
// command), and what was made is removed; services beside a run that is
// not a Docker run, or beside a network the caller chose, are refused
// before anything is made. A service that exits says why: its exit code
// and its last log lines ("it exited 1: … chdir /data …"), or that its log
// could not be read — never a guess.
func TestARunWhoseServicesAreNotReadyDoesNotStartAndLeavesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake, calls := filepath.Join(dir, "docker"), filepath.Join(dir, "calls")
	// This executable is a local recording stand-in; no Docker daemon is used.
	nolog := filepath.Join(dir, "nolog")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$1 $5" in
"inspect {{.State.ExitCode}}") echo 1; exit 0;;
esac
case "$1" in
inspect) echo "exited none"; exit 0;;
logs) [ -e "` + nolog + `" ] && { echo "no such container" >&2; exit 1; }; echo "starting"; echo "chdir /data: no such file or directory"; exit 0;;
esac
exit 0
`
	if err := execfile.Write(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine"}}
	req := Request{Argv: []string{"make", "test"}, Image: "agent:test", Name: "tests-1", Timeout: polltest.HangGuard(t), Services: svc}
	out := (Runner{DockerBinary: fake}).Run(context.Background(), Docker, req)
	if out.Termination != LaunchFailed || out.RunErr == nil || !strings.Contains(out.RunErr.Error(), "the service postgres: it exited 1: starting / chdir /data: no such file or directory") {
		t.Fatalf("a run whose service never became ready: %+v", out)
	}
	if err := os.WriteFile(nolog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := (Runner{DockerBinary: fake}).Run(context.Background(), Docker, req); out.RunErr == nil || !strings.Contains(out.RunErr.Error(), "it exited 1, and its log could not be read") {
		t.Fatalf("a service whose log could not be read: %+v", out)
	}
	_ = os.Remove(nolog)
	recorded, _ := os.ReadFile(calls)
	if strings.Contains(string(recorded), "run --rm") {
		t.Fatalf("the command ran without its services: %s", recorded)
	}
	if !strings.Contains(string(recorded), "rm -f -v tests-1-postgres") || !strings.Contains(string(recorded), "network rm tests-1-net") {
		t.Fatalf("what the failed start made was left behind: %s", recorded)
	}
	if out := (Runner{DockerBinary: fake}).Run(context.Background(), Local, req); out.Termination != LaunchFailed {
		t.Fatalf("services beside a local process: %+v", out)
	}
	req.Network = "host"
	_ = os.Remove(calls)
	if out := (Runner{DockerBinary: fake}).Run(context.Background(), Docker, req); out.Termination != LaunchFailed {
		t.Fatalf("services beside a network the caller chose: %+v", out)
	}
	if recorded, _ := os.ReadFile(calls); strings.Contains(string(recorded), "create") {
		t.Fatalf("a refused request made something: %s", recorded)
	}
}

// Each service runs under limits, so one project's test database cannot
// starve what else runs on the machine: the defaults when the runner names
// none, its own when it does (a worker's -service-* flags), memory with
// swap included. A limit with a zero is refused before anything is made:
// zero is never "unlimited". A service that keeps data gets a disposable
// folder there (-v <path>, an anonymous volume the removal's -v takes);
// S3's emulator exits at once without /data. One with no data path gets
// none. Each keeps a bounded log (json-file, 10 MiB, three files).
func TestServicesRunUnderTheDefaultLimitsOrTheRunnersOwn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake, calls := filepath.Join(dir, "docker"), filepath.Join(dir, "calls")
	// This executable is a local recording stand-in; no Docker daemon is used.
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$1" in
inspect) case "$3" in *OOMKilled*) echo false;; *) echo "running none";; esac; exit 0;;
esac
exit 0
`
	if err := execfile.Write(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Argv: []string{"make", "test"}, Image: "agent:test", Name: "tests-1", Timeout: polltest.HangGuard(t),
		Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine"}, {Name: "s3", Image: "versity/versitygw:v1.8.0", Data: "/data", Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{"start"}}}}
	serviceRuns := func(r Runner) []string {
		t.Helper()
		_ = os.Remove(calls)
		if out := r.Run(context.Background(), Docker, req); !out.Exited0() {
			t.Fatalf("the run with services: %+v", out)
		}
		recorded, _ := os.ReadFile(calls)
		var runs []string
		for _, l := range strings.Split(string(recorded), "\n") {
			if strings.HasPrefix(l, "run -d") {
				runs = append(runs, l)
			}
		}
		if len(runs) != 2 {
			t.Fatalf("services started: %s", recorded)
		}
		return runs
	}
	for _, run := range serviceRuns(Runner{DockerBinary: fake}) {
		if !strings.Contains(run, "--cpus 1 --memory 1073741824 --memory-swap 1073741824 --pids-limit 256") {
			t.Errorf("a service without the default limits: %s", run)
		}
		if !strings.Contains(run, "--log-driver json-file --log-opt max-size=10m --log-opt max-file=3") {
			t.Errorf("a service without a bounded log: %s", run)
		}
		switch keeps := strings.Contains(run, "--network-alias s3 "); {
		case keeps && !strings.HasSuffix(run, " -v /data --entrypoint /bin/sh versity/versitygw:v1.8.0 -c start"):
			t.Errorf("s3 started without a folder at /data, or not by its script: %s", run)
		case !keeps && strings.Contains(run, " -v "):
			t.Errorf("a service with no data path got a volume: %s", run)
		}
	}
	if recorded, _ := os.ReadFile(calls); !strings.Contains(string(recorded), "rm -f -v tests-1-s3") {
		t.Errorf("the service was not removed with its volume: %s", recorded)
	}
	own := Runner{DockerBinary: fake, ServiceLimits: Limits{CPUs: 0.5, MemoryBytes: 256 << 20, PIDs: 64}}
	for _, run := range serviceRuns(own) {
		if !strings.Contains(run, "--cpus 0.5 --memory 268435456 --memory-swap 268435456 --pids-limit 64") {
			t.Errorf("a service without the runner's limits: %s", run)
		}
	}
	_ = os.Remove(calls)
	zero := Runner{DockerBinary: fake, ServiceLimits: Limits{CPUs: 1, MemoryBytes: 1 << 30}}
	if out := zero.Run(context.Background(), Docker, req); out.Termination != LaunchFailed {
		t.Fatalf("a service limit of zero: %+v", out)
	}
	if recorded, _ := os.ReadFile(calls); strings.Contains(string(recorded), "run") || strings.Contains(string(recorded), "create") {
		t.Fatalf("a refused limit made something: %s", recorded)
	}
}

// A service or network is gone only when the daemon answers so. A cleanup
// that could not reach the daemon (its socket missing: "no such file or
// directory") is not a cleanup that succeeded, and makes the run no answer.
func TestServiceCleanupCountsOnlyTheDaemonsAnswerAsGone(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		answer    string
		wantClean bool
	}{
		"the daemon says it is gone": {"Error response from daemon: No such container: tests-1-postgres", true},
		"the daemon cannot be asked": {"error during connect: dial unix /var/run/docker.sock: connect: no such file or directory", false},
		"the CLI is not installed":   {"docker: command not found", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := filepath.Join(dir, "docker")
			// This executable is a local stand-in; no Docker daemon is used.
			script := `#!/bin/sh
case "$1" in
inspect) case "$3" in *OOMKilled*) echo false;; *) echo "running none";; esac; exit 0;;
rm) echo "` + tc.answer + `" >&2; exit 1;;
network) [ "$2" = rm ] && { echo "` + tc.answer + `" >&2; exit 1; }; exit 0;;
esac
exit 0
`
			if err := execfile.Write(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			req := Request{Argv: []string{"make", "test"}, Image: "agent:test", Name: "tests-1", Timeout: polltest.HangGuard(t),
				Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine"}}}
			out := (Runner{DockerBinary: fake}).Run(context.Background(), Docker, req)
			if out.Termination != Exited || (out.CleanupErr == nil) != tc.wantClean {
				t.Fatalf("%s: %+v; want clean %v", tc.answer, out, tc.wantClean)
			}
		})
	}
}
