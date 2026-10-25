package docker_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestStopStepReportsOnlyConfirmedRemovals(t *testing.T) {
	t.Parallel()
	step := idstest.N(7)
	agent := fmt.Sprintf("apptown-step-%d-agent", step)
	second := fmt.Sprintf("apptown-step-%d-second", step)
	review := fmt.Sprintf("apptown-review-%d-reviewer", step)
	failed := errors.New("daemon unavailable")
	for _, tc := range []struct {
		name, inventoryFailure, removalFailure string
		agents, reviews, want, removals        []string
	}{
		{name: "agent inventory failure", inventoryFailure: "apptown-step-"},
		{name: "first removal failure", agents: []string{agent}, removalFailure: agent, removals: []string{agent}},
		{name: "partial removal failure", agents: []string{agent, second}, removalFailure: second, want: []string{agent}, removals: []string{agent, second}},
		{name: "review inventory failure", agents: []string{agent}, inventoryFailure: "apptown-review-", want: []string{agent}, removals: []string{agent}},
		{name: "successful agent and review", agents: []string{agent}, reviews: []string{review}, want: []string{agent, review}, removals: []string{agent, review}},
		{name: "empty"},
		{name: "unrelated names", agents: []string{"foreign-" + agent}, reviews: []string{agent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			daemon := dockerfake.New()
			var removals []string
			daemon.SetReply(func(args []string) ([]byte, error, bool) {
				switch args[0] {
				case "ps":
					joined := strings.Join(args, " ")
					if tc.inventoryFailure != "" && strings.Contains(joined, tc.inventoryFailure) {
						return []byte(agent), failed, true
					}
					names := tc.agents
					if strings.Contains(joined, "name=apptown-review-") {
						names = tc.reviews
					}
					return []byte(strings.Join(names, "\n")), nil, true
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
			wantErr := tc.inventoryFailure != "" || tc.removalFailure != ""
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
