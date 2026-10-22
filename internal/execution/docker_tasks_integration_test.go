//go:build integration

package execution_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executiontest"
)

func dockerSpec(key, output string, exit int, block bool) execution.TaskSpec {
	script := fmt.Sprintf("printf %%s '%s'; exit %d", output, exit)
	if block {
		script = "sleep 300"
	}
	return execution.TaskSpec{Key: key, Image: "busybox:latest", Argv: []string{"sh", "-c", script}, Labels: []string{"apptown.class=test"},
		Admission: execution.Admission{Profile: "local-docker", Network: execution.NetworkNone, Limits: execution.Limits{CPUs: 1, MemoryBytes: 64 << 20, PIDs: 64}}}
}

// Local Docker keeps the task contract with real containers, including the
// admission's limits and network policy.
func TestIntegrationDockerTasksKeepTheContract(t *testing.T) {
	executiontest.Run(t, executiontest.Harness{Driver: execution.DockerTasks{}, Spec: dockerSpec, Key: executiontest.UniqueKeys("conformance")})
}

// The admission's limits and network policy reach the container Docker runs.
func TestIntegrationDockerTasksApplyTheAdmission(t *testing.T) {
	d := execution.DockerTasks{}
	key := executiontest.UniqueKeys("admission")("limits")
	t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
	created, err := d.Ensure(context.Background(), dockerSpec(key, "", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.PidsLimit}} {{.HostConfig.NetworkMode}}", created.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != fmt.Sprintf("1000000000 %d 64 none", 64<<20) {
		t.Fatalf("the container runs with %q", got)
	}
}

// A repeated Start after a lost response must not execute an exited task again.
func TestIntegrationADockerTaskIsNotRunAgainByASecondStart(t *testing.T) {
	ctx := context.Background()
	d := execution.DockerTasks{}
	key := executiontest.UniqueKeys("start")("once")
	t.Cleanup(func() { _ = d.Release(ctx, execution.TaskRef{Key: key}) })
	obs, err := d.Ensure(ctx, dockerSpec(key, "executed\n", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: obs.ID}
	for range 2 {
		if err := d.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Wait(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	out, err := d.Output(ctx, ref, execution.OutputRead{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out.Data), "executed"); n != 1 {
		t.Fatalf("repeated Start executed the task %d times; output=%q", n, out.Data)
	}
}

// Identity must bind the operation to its request, not just a container name.
func TestIntegrationDockerRefusesADifferentRequestUnderAKey(t *testing.T) {
	ctx := context.Background()
	d := execution.DockerTasks{}
	key := executiontest.UniqueKeys("digest")("conflict")
	t.Cleanup(func() { _ = d.Release(ctx, execution.TaskRef{Key: key}) })
	first := dockerSpec(key, "first", 0, false)
	if _, err := d.Ensure(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed := dockerSpec(key, "different", 0, false)
	changed.Admission.Profile = "another-profile"
	changed.Admission.Network = execution.NetworkDefault
	if got, err := d.Ensure(ctx, changed); err == nil {
		t.Fatalf("changed request adopted old task: %+v", got)
	}
}

// Local client configuration failure is not an authoritative Docker absence.
func TestIntegrationADockerClientFailureIsNotAbsence(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2376")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", t.TempDir()) // No certificates: Docker never reaches a daemon.
	d := execution.DockerTasks{}
	ctx := context.Background()
	ref := execution.TaskRef{Key: "client-unavailable"}
	obs, err := d.Observe(ctx, ref)
	if !errors.Is(err, execution.ErrTaskUnobservable) {
		t.Errorf("client failure became observation=%+v err=%v; want unobservable", obs, err)
	}
	if err := d.Stop(ctx, ref); err == nil {
		t.Error("Stop reported success without reaching Docker")
	}
	if err := d.Release(ctx, ref); err == nil {
		t.Error("Release reported success without reaching Docker")
	}
}

// Two installations sharing a Docker daemon keep their tasks apart: the same
// key in each names a different container.
func TestIntegrationInstallationsSharingDockerKeepTheirTasksApart(t *testing.T) {
	ctx := context.Background()
	key := executiontest.UniqueKeys("shared")("same-key")
	first, second := execution.DockerTasks{Namespace: "installation-a"}, execution.DockerTasks{Namespace: "installation-b"}
	t.Cleanup(func() {
		_ = first.Release(context.Background(), execution.TaskRef{Key: key})
		_ = second.Release(context.Background(), execution.TaskRef{Key: key})
	})
	a, err := first.Ensure(ctx, dockerSpec(key, "a", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Ensure(ctx, dockerSpec(key, "b", 0, false))
	if err != nil || b.ID == a.ID {
		t.Fatalf("the second installation's task: %+v %v (first %s)", b, err, a.ID)
	}
	if err := first.Release(ctx, execution.TaskRef{Key: key}); err != nil {
		t.Fatal(err)
	}
	if still, err := second.Observe(ctx, execution.TaskRef{Key: key}); err != nil || still.ID != b.ID {
		t.Fatalf("releasing one installation's task touched the other's: %+v %v", still, err)
	}
}
