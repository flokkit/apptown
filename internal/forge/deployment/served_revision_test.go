package deployment

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/dockerlabel"
	"flokkit.io/forge/internal/ids/idstest"
)

// The commit a deployment serves is named only when the container on its
// own target runs and carries that deployment's settled generation: a
// landing's container names the landed commit; the same name holding a
// container of another generation, a stopped container, or a missing one
// names no commit, with the reason. Verification compares a change's
// running-app findings with main only on this witness.
//
// Kills: naming the recorded commit without asking the daemon; trusting a
// container by name alone; a stopped or missing container read as serving.
func TestAServedRevisionIsWitnessedOnTheRunningContainer(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(true)
	main := w.deployment(t, s, "main", w.remote)
	w.land(t, s, main, idstest.N(7))
	landed, err := s.Deployments.ByID(context.Background(), main.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := s.ServedRevision(ctx, landed); got.Commit != w.revision || got.Reason != "" {
		t.Fatalf("the landed container serves %s: %+v", w.revision, got)
	}

	// Another generation's container under the same name is not the one
	// the record names.
	stale := *landed
	stale.ObservedGeneration++
	if got := s.ServedRevision(ctx, &stale); got.Commit != "" || !strings.Contains(got.Reason, "not the generation") {
		t.Fatalf("a container of another generation: %+v", got)
	}

	// Retained deployments carry the exact pre-cutover host identity. A
	// numeric label is not sufficient, nor may the UUID replace a retained
	// label merely because both identify this deployment in other contexts.
	for _, tc := range []struct {
		name, hostLabel, containerLabel string
		generation                      int64
		serves                          bool
	}{
		{"retained host label", "129", "129", landed.ObservedGeneration, true},
		{"other numeric identity", "129", "130", landed.ObservedGeneration, false},
		{"UUID instead of retained identity", "129", landed.ID.String(), landed.ObservedGeneration, false},
		{"retained identity wrong generation", "129", "129", landed.ObservedGeneration + 1, false},
		{"numeric identity without recorded host label", "", "129", landed.ObservedGeneration, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := *landed
			d.HostLabel = tc.hostLabel
			w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
				if len(args) != 4 || args[0] != "inspect" || args[2] != docker.RuntimeFormat || args[3] != d.Container {
					return nil, nil, false
				}
				rt := docker.Runtime{ID: "retained-container", Running: true, Labels: map[string]string{
					dockerlabel.Deployment: tc.containerLabel,
					dockerlabel.Generation: strconv.FormatInt(tc.generation, 10),
				}}
				out, err := json.Marshal(rt)
				return out, err, true
			})
			got := s.ServedRevision(ctx, &d)
			if tc.serves {
				if got.Commit != w.revision || got.ContainerID != "retained-container" || got.Reason != "" {
					t.Fatalf("the retained container serves the recorded commit: %+v", got)
				}
			} else if got.Commit != "" || got.ContainerID != "retained-container" || !strings.Contains(got.Reason, "not the generation") {
				t.Fatalf("a mismatched container serves no recorded commit: %+v", got)
			}
		})
	}
	w.daemon.SetReply(nil)

	w.daemon.SetRunning(landed.Container, false)
	if got := s.ServedRevision(ctx, landed); got.Commit != "" || !strings.Contains(got.Reason, "not running") {
		t.Fatalf("a stopped container serves nothing: %+v", got)
	}
	w.daemon.remove(landed.Container)
	if got := s.ServedRevision(ctx, landed); got.Commit != "" || got.Reason == "" {
		t.Fatalf("a missing container serves nothing: %+v", got)
	}
	unrecorded := *landed
	unrecorded.ReleaseSHA = ""
	if got := s.ServedRevision(ctx, &unrecorded); got.Commit != "" || !strings.Contains(got.Reason, "records no commit") {
		t.Fatalf("a deployment that records no commit: %+v", got)
	}
}
