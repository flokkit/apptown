//go:build integration

package execution_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executiontest"
)

// A real name conflict followed by a failed observation does not establish
// which request owns the container. Preserve the unanswered classification.
func TestIntegrationAnUnansweredAdoptionCheckIsUnobservable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TEST_DOCKER_BIN", binary)
	wrapper := filepath.Join(dir, "docker")
	if err := execfile.Write(wrapper, []byte(`#!/bin/sh
if [ "$1" = inspect ]; then
 echo 'Cannot connect to the Docker daemon' >&2
 exit 1
fi
exec "$TEST_DOCKER_BIN" "$@"
`), 0755); err != nil {
		t.Fatal(err)
	}
	key := executiontest.UniqueKeys("round3")("conflict")
	spec := dockerSpec(key, "once", 0, false)
	d := execution.DockerTasks{}
	obs, err := d.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key, ProviderID: obs.ID}) })
	_, err = (execution.DockerTasks{Binary: wrapper}).Ensure(ctx, spec)
	if !errors.Is(err, execution.ErrTaskUnobservable) {
		t.Fatalf("failed adoption observation becomes authoritative failure: %v", err)
	}
}

// Key and provider ID are both supported ways to address the same task.
// Starting by each must share one lock, including across driver instances.
func TestIntegrationStartsByKeyAndByIDRunATaskOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TEST_DOCKER_BIN", binary)
	t.Setenv("TEST_BARRIER", dir)
	wrapper := filepath.Join(dir, "docker")
	if err := execfile.Write(wrapper, []byte(`#!/bin/sh
if [ "$1" = inspect ]; then
 "$TEST_DOCKER_BIN" "$@" > "$TEST_BARRIER/observed" || exit $?
 touch "$TEST_BARRIER/ready"
 while [ ! -e "$TEST_BARRIER/release" ]; do sleep 0.01; done
 cat "$TEST_BARRIER/observed"
 exit 0
fi
exec "$TEST_DOCKER_BIN" "$@"
`), 0755); err != nil {
		t.Fatal(err)
	}
	key := executiontest.UniqueKeys("round3")("aliases")
	d := execution.DockerTasks{}
	obs, err := d.Ensure(ctx, dockerSpec(key, "executed\n", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: obs.ID}
	t.Cleanup(func() { _ = d.Release(context.Background(), ref) })
	done := make(chan error, 1)
	go func() { done <- (execution.DockerTasks{Binary: wrapper}).Start(ctx, ref) }()
	defer func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600) }()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	second := make(chan error, 1)
	go func() {
		err := d.Start(ctx, execution.TaskRef{ProviderID: obs.ID})
		if err == nil {
			_, err = d.Wait(ctx, ref)
		}
		second <- err
	}()
	// The unfixed path completes before the first is released. A correctly
	// serialized implementation may wait; release it after a bounded interval.
	finished := false
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
		finished = true
	case <-time.After(time.Second):
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !finished {
		if err := <-second; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Wait(ctx, ref); err != nil {
		t.Fatal(err)
	}
	page, err := d.Output(ctx, ref, execution.OutputRead{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(page.Data), "executed"); n != 1 {
		t.Fatalf("same-process starts by key and ID executed %d times: %q", n, page.Data)
	}
}

// A pending Start must honor its own deadline while another Start holds the
// serialization lock; the first caller's lifetime is not the second's budget.
func TestIntegrationAWaitingStartHonoursItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TEST_DOCKER_BIN", binary)
	t.Setenv("TEST_BARRIER", dir)
	wrapper := filepath.Join(dir, "docker")
	if err := execfile.Write(wrapper, []byte(`#!/bin/sh
if [ "$1" = inspect ]; then
 "$TEST_DOCKER_BIN" "$@" > "$TEST_BARRIER/observed" || exit $?
 touch "$TEST_BARRIER/ready"
 while [ ! -e "$TEST_BARRIER/release" ]; do sleep 0.01; done
 cat "$TEST_BARRIER/observed"
 exit 0
fi
exec "$TEST_DOCKER_BIN" "$@"
`), 0755); err != nil {
		t.Fatal(err)
	}
	key := executiontest.UniqueKeys("round3")("cancel")
	d := execution.DockerTasks{}
	obs, err := d.Ensure(ctx, dockerSpec(key, "executed\n", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: obs.ID}
	t.Cleanup(func() { _ = d.Release(context.Background(), ref) })
	first := make(chan error, 1)
	go func() { first <- (execution.DockerTasks{Binary: wrapper}).Start(ctx, ref) }()
	// Release and join both starts before the container cleanup, even on failure.
	var second chan error
	secondJoined := false
	defer func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
		<-first
		if second != nil && !secondJoined {
			<-second
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	second = make(chan error, 1)
	go func() { second <- d.Start(short, ref) }()
	select {
	case err := <-second:
		secondJoined = true
		if err == nil {
			t.Fatal("deadline-expired Start succeeded")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Start still waits for another caller's lock after its own deadline")
	}
}
