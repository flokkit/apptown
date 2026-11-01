package docker_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Every container a step runs is named execution.StepContainer, so
// stopping the step removes all of them, its scans, tests and conformance
// checks as well as its agent and reviewer; it reports only the removals
// Docker confirmed.
func TestStopStepRemovesEveryContainerTheStepRuns(t *testing.T) {
	t.Parallel()
	step := idstest.N(7)
	agent := execution.UniqueName(execution.StepContainer(step, "agent"))
	review := execution.UniqueName(execution.StepContainer(step, "review"))
	scan := execution.UniqueName(execution.StepContainer(step, "scan"))
	tests := execution.UniqueName(execution.StepContainer(step, "tests-unit"))
	conform := execution.StepContainer(step, "conform-base")
	other := execution.UniqueName(execution.StepContainer(idstest.N(8), "agent"))
	failed := errors.New("daemon unavailable")
	for _, tc := range []struct {
		name, removalFailure string
		listFails            bool
		running, want        []string
		removals             []string
	}{
		{name: "inventory failure", listFails: true},
		{name: "first removal failure", running: []string{agent}, removalFailure: agent, removals: []string{agent}},
		{name: "partial removal failure", running: []string{agent, review}, removalFailure: review, want: []string{agent}, removals: []string{agent, review}},
		{name: "every kind of container", running: []string{agent, review, scan, tests, conform}, want: []string{agent, review, scan, tests, conform}, removals: []string{agent, review, scan, tests, conform}},
		{name: "empty"},
		{name: "unrelated names", running: []string{"foreign-" + agent, other}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			daemon := newDockerFake()
			var removals []string
			daemon.SetReply(func(args []string) ([]byte, error, bool) {
				switch args[0] {
				case "ps":
					if !strings.Contains(strings.Join(args, " "), "name="+execution.StepContainerPrefix(step)) {
						t.Errorf("listed by another name: %v", args)
					}
					if tc.listFails {
						return []byte(agent), failed, true
					}
					return []byte(strings.Join(tc.running, "\n")), nil, true
				case "rm":
					name := args[len(args)-1]
					removals = append(removals, name)
					if name == tc.removalFailure {
						return nil, failed, true
					}
					return nil, nil, true
				}
				return nil, nil, false
			})
			got, err := (docker.Scope{Client: daemon}).StopStep(context.Background(), step)
			wantErr := tc.listFails || tc.removalFailure != ""
			if errors.Is(err, failed) != wantErr || (err != nil) != wantErr {
				t.Fatalf("error=%v, want failure=%v", err, wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("confirmed removals=%v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(removals, tc.removals) {
				t.Fatalf("attempted removals=%v, want %v", removals, tc.removals)
			}
		})
	}
}

func newDockerFake() *dockerfake.Daemon {
	return dockerfake.New()
}
