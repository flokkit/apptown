//go:build integration

package apptown

import (
	"context"
	"errors"
	"fmt"
	dockercli "github.com/flokkit/apptown/internal/deploy/docker"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// The composed proof of private Docker on a worker. What is real and what is controlled:
//   - real: apptown-worker's server in this process, over mutual TLS, driving
//     this machine's Docker: the agent's container, its privileged private
//     daemon (docker:27-dind), the service the app's need resolves to from
//     the catalog, the nested build and run inside the daemon, and the
//     tests step's container; the worker's measured profile;
//   - controlled: Pi. The agent image is the Docker CLI's, whose `pi` is a
//     script (fakePiScript) that runs the change's fake-pi.sh;
//   - refused: this machine's Docker and process runner, which record every
//     call (any call but a container listing fails the test), and the
//     AppTown's own agent process (localTripwire).
//
// This is dispatch confinement, not host separation: the worker drives the
// same Docker Desktop VM as everything else on this machine. It shows that
// nothing the workflow runs is dispatched to AppTown's own Docker, not
// that the worker is another machine.
//
// One workflow of an implementation step declaring docker: true and the
// app's needs, then a tests step: the agent builds an image and runs it,
// runs a container with its tree at the same path, and reaches the need's
// service by name; its work is published; the tests run over it and
// satisfy the stage's gate, with their placement recorded: the same
// worker. The implementation's evidence records the worker's profile.
//
// With APPTOWN_EXTERNAL_WORKER_URL (workertest.External), the worker is
// instead a real apptown-worker process on another machine, sharing no
// files with this one.
func TestIntegrationAnImplementationStepBuildsWithPrivateDockerOnAWorker(t *testing.T) {
	requireDocker(t)
	agentContext, image := composedAgentImage(t)
	external := workertest.LookupExternal(t)
	var version []byte
	if external == nil {
		v, err := dockertest.Output("version", "--format", "{{.Server.Version}}")
		if err != nil {
			t.Fatal(err)
		}
		version = v
	} else {
		profile, err := external.Driver(t).Profile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		version = []byte(profile.Docker)
	}
	{
		t.Run("checks on the same worker", func(t *testing.T) {
			ctx := context.Background()
			ca := workertest.NewCA(t)
			var agentChecks atomic.Int64
			counting := func(n *atomic.Int64) func(http.Handler) http.Handler {
				return func(h http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/v1/checks" {
							n.Add(1)
						}
						h.ServeHTTP(w, r)
					})
				}
			}
			cfg := Config{DataDir: t.TempDir(), AgentImage: "apptown-agent", AgentContext: agentContext, ConsoleCLI: fixtureCLI(t), PrivateDocker: true}
			if external != nil {
				// Each variant is a new AppTown, whose task keys are its own:
				// its workers are provisioned again for it.
				t.Logf("the workers: %s", external.Ctl(t, "fresh"))
				cfg.Worker = &WorkerConfig{URL: external.URL, Cert: external.Cert, Key: external.Key, CA: external.CA}
			} else {
				namespace := "privproof" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
				removeTaskContainers(t, namespace)
				agent := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(),
					Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: namespace}, AgentImage: image, ImageLabels: worker.DockerImages{Docker: dockercli.LocalCLI("docker")},
					DindImage: "docker:27-dind", Docker: docker.CLI{Binary: "docker"}, Checks: execution.Runner{DockerBinary: "docker"}}
				agent.Profile = agent.MeasureProfile(ctx, true)
				removePrivateDockerOf(t, agent)
				agentSrv := workertest.StartServerWith(t, ca, agent, counting(&agentChecks))
				cfg.Worker = workerClientConfig(t, ca, agentSrv.URL)
			}
			a := testApp(t, cfg)
			a.tasks.Poll, a.tasks.Patience = 100*time.Millisecond, 2*time.Second
			calls := filepath.Join(t.TempDir(), "calls")
			localDockerTripwire(t, a, calls)
			recomposeChecks(t, a)
			if _, err := a.Dependencies.Save(ctx, kvKind("redis:7-alpine", "redis"), "tester"); err != nil {
				t.Fatal(err)
			}
			run, steps := agentWorkerChange(t, a, []schema.Step{
				{Name: "implement", Uses: "agent/pi", With: map[string]any{"prompt": "build it", "executor": "docker", "docker": true, "needs": true}},
				{Name: "tests", Uses: "tests", With: map[string]any{"command": `grep -q "hello from a nested build" built.txt && grep -q nested nested.txt && grep -q reached kv.txt && echo "the agent's work holds"`}},
			}, map[string]string{
				"Dockerfile":        "FROM " + stock.Images().DF + "\nCOPY greeting.txt /greeting.txt\nCMD [\"cat\", \"/greeting.txt\"]\n",
				"greeting.txt":      "hello from a nested build\n",
				".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  kv:\n    flavour: sourdough\n",
				"fake-pi.sh": `docker build -q -t composed-greeting /work >/dev/null 2>build.log && docker run --rm composed-greeting > built.txt
docker run --rm -v /work:/work ` + stock.Images().DF + ` sh -c 'echo nested > /work/nested.txt'
nc -z kv 6379 && echo reached > kv.txt
echo "$KV_URL" > url.txt
say "built, ran and reached the service"`,
			})
			onlyOnTheWorker(t, a)

			if err := a.Work.ExecuteStep(a.asEngine(ctx), steps["implement"].ID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, steps["implement"].ID)
			if status != workflow.StatusPass {
				t.Fatalf("the implementation step is %s: %v", status, ev)
			}
			source := workflowSourceForTest(t, a, run)
			hub := requireLocation(t)(source.Repository(a.cfg.dataDir))
			for file, want := range map[string]string{"built.txt": "hello from a nested build", "nested.txt": "nested", "kv.txt": "reached", "url.txt": "redis://kv/sourdough"} {
				if got := gittest.Run(t, hub, "show", scm.Ref(ctx, hub, "refs/heads/"+source.Branch)+":"+file); got != want {
					t.Errorf("%s on the change's branch: %q, want %q", file, got, want)
				}
			}
			profile, _ := ev["workerProfile"].(map[string]any)
			t.Logf("the attempt's evidence: workerProfile %v", profile)
			if profile["name"] != "worker-vm" || profile["docker"] != strings.TrimSpace(string(version)) || profile["privileged"] != "yes" ||
				profile["exclusive"] != true || profile["egress"] != "not contained" || profile["dindImage"] != "docker:27-dind" ||
				!strings.HasPrefix(fmt.Sprint(profile["dindImageId"]), "sha256:") || !strings.HasPrefix(fmt.Sprint(profile["dindDigest"]), "docker@sha256:") ||
				!strings.HasPrefix(fmt.Sprint(profile["dindVersion"]), "27.") {
				t.Errorf("the evidence's profile: %v", profile)
			}

			if err := a.Work.ExecuteStep(a.asEngine(ctx), steps["tests"].ID); err != nil {
				t.Fatal(err)
			}
			tests := readStep(t, a, steps["tests"].ID)
			want := workflow.Placement{Worker: a.cfg.worker.URL}
			if p := placementOf(t, tests); tests.Status != workflow.StatusPass || p == nil || *p != want {
				t.Fatalf("the tests: %s %s", tests.Status, tests.Evidence)
			}
			switch {
			case external != nil: // its requests are not counted here; the placement says where they ran
			case agentChecks.Load() == 0:
				t.Error("the tests did not run on the worker")
			}
			if out, err := a.Work.Advance(a.asEngine(ctx), run.ID); err != nil || len(out.Missing) != 0 || !out.Advanced {
				t.Fatalf("the stage's gate: %+v %v", out, err)
			}
			recorded, _ := os.ReadFile(calls)
			for _, line := range strings.Split(string(recorded), "\n") {
				if strings.HasPrefix(line, "local ") && !strings.HasPrefix(line, "local ps ") {
					t.Fatalf("this machine's Docker was asked: %s\n%s", line, recorded)
				}
			}
		})
	}
}

// composedAgentImage builds an agent image from the Docker CLI's, whose
// `pi` is fakePiScript, from a build context named as a worker names its
// agent image (worker.AgentImage). It answers the context and the image.
func composedAgentImage(t *testing.T) (dir, image string) {
	t.Helper()
	dir = t.TempDir()
	files := map[string]string{"Dockerfile": "FROM docker:27-dind\nENTRYPOINT [\"docker-entrypoint.sh\"]\nLABEL apptown.pi.version=\"" + fakePiVersion + "\"\nCOPY pi /usr/local/bin/pi\n", "pi": fakePiScript}
	for name, body := range files {
		if err := execfile.Write(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	image, err := worker.AgentImage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dockertest.Run("image", "inspect", image) == nil {
		return dir, image
	}
	if out, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: []string{"build", "-q", "-t", image, dir}, Env: []string{"BUILDX_CONFIG=" + filepath.Join(t.TempDir(), "buildx")}}); err != nil {
		t.Fatalf("building %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("image", "rm", image) })
	return dir, image
}

// removePrivateDockerOf removes, when the test ends, whatever private Docker
// the worker left (only what carries its label), however the test ended:
// it is registered before the worker can make any. A container or network
// still there afterwards is reported, so a leak fails the test instead of
// running on.
//
// The worker's label is read when the test ends, not when this is called:
// it depends on the worker's installation, which starting it may set. A
// worker that still has none was never started as one, and its label names
// nothing it made, so the test fails instead of finding nothing.
func removePrivateDockerOf(t *testing.T, w *worker.Server) {
	t.Helper()
	removeDockerOwnedBy(t, func() (string, error) {
		if w.Installation == "" {
			return "", errors.New("the worker has no installation: its label is not the one its private Docker carries")
		}
		return w.CheckOwnerLabel(), nil
	})
}

// removeDockerOwnedBy removes every container (with its volumes) and network
// labelled with what label answers when the test ends, and fails the test if
// any remain or the label cannot be known.
func removeDockerOwnedBy(t *testing.T, label func() (string, error)) {
	t.Helper()
	t.Cleanup(func() {
		owner, err := label()
		if err != nil || owner == "" {
			t.Errorf("the private Docker to remove cannot be named: %q %v", owner, err)
			return
		}
		var left []string
		if !poll.Until(context.Background(), time.Minute, time.Second, func() bool {
			left = dockerOwnedBy(owner)
			for _, id := range left {
				if strings.HasPrefix(id, "network ") {
					_ = dockertest.Run("network", "rm", strings.TrimPrefix(id, "network "))
				} else {
					_ = dockertest.Run("rm", "-f", "-v", id)
				}
			}
			left = dockerOwnedBy(owner)
			return len(left) == 0
		}) {
			t.Errorf("the test left Docker labelled %s behind: %v", owner, left)
		}
	})
}

// dockerOwnedBy lists the containers, then the networks ("network <id>"),
// labelled owner; one that cannot be listed is reported as left.
func dockerOwnedBy(owner string) []string {
	var left []string
	out, err := dockertest.Output("ps", "-aq", "--filter", "label="+owner)
	if err != nil {
		return []string{fmt.Sprintf("(containers unlisted: %v)", err)}
	}
	left = append(left, strings.Fields(string(out))...)
	out, err = dockertest.Output("network", "ls", "-q", "--filter", "label="+owner)
	if err != nil {
		return append(left, fmt.Sprintf("(networks unlisted: %v)", err))
	}
	for _, id := range strings.Fields(string(out)) {
		left = append(left, "network "+id)
	}
	return left
}

// failingWithPrivateDocker marks TestIntegrationPrivateDockerIsRemovedWhenATestFails's
// child run.
const failingWithPrivateDocker = "APPTOWN_TEST_FAILING_PRIVATE_DOCKER"

// A test that fails after its worker made private Docker leaves none of it
// running. The child run is a worker step world's sequence: the removal is
// registered on a worker not yet started (its installation unset, as
// newWorkerStepWorld builds it), the worker starts, makes a labelled
// container and network with the label it then carries, and the test
// fails. The parent sees it failed and finds nothing of it left.
func TestIntegrationPrivateDockerIsRemovedWhenATestFails(t *testing.T) {
	requireDocker(t)
	if os.Getenv(failingWithPrivateDocker) != "" {
		server := &worker.Server{ToolVersions: stock.Versions(), State: t.TempDir()}
		removePrivateDockerOf(t, server)
		workertest.StartServerWith(t, workertest.NewCA(t), server, nil)
		owner := server.CheckOwnerLabel()
		fmt.Printf("private-docker-owner=%s\n", owner)
		if out, err := dockertest.CombinedOutput("network", "create", "--label", owner, "apptown-private-cleanup-"+strings.TrimPrefix(owner, "apptown.worker=")); err != nil {
			t.Fatalf("network: %v %s", err, out)
		}
		if out, err := dockertest.CombinedOutput("run", "-d", "--label", owner, stock.Images().DF, "sleep", "300"); err != nil {
			t.Fatalf("container: %v %s", err, out)
		}
		t.Fatal("the test fails after its worker made private Docker")
	}
	child := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv:    []string{os.Args[0], "-test.run=^TestIntegrationPrivateDockerIsRemovedWhenATestFails$", "-test.count=1", "-test.v"},
		Env:     append(os.Environ(), failingWithPrivateDocker+"=1"),
		Timeout: 5 * time.Minute,
	})
	owner := ""
	for _, line := range strings.Split(string(child.Output), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "private-docker-owner="); ok {
			owner = v
		}
	}
	if owner == "" {
		t.Fatalf("the child never said its worker's label:\n%s", child.Output)
	}
	// Whatever the child left is removed here, by the label it carried.
	removeDockerOwnedBy(t, func() (string, error) { return owner, nil })
	if child.Termination != execution.Exited || child.ExitCode == 0 || !strings.Contains(string(child.Output), "the test fails after its worker made private Docker") {
		t.Fatalf("the child did not fail as it should: %s %d %v\n%s", child.Termination, child.ExitCode, child.RunErr, child.Output)
	}
	if left := dockerOwnedBy(owner); len(left) != 0 {
		t.Fatalf("a failed test left its private Docker running: %v\n%s", left, child.Output)
	}
}
