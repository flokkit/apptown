package deployment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A real Docker inventory must batch the sweep, including a one-app host.
// A delayed host reading is unknown for every app, retaining the last reading.
func TestDockerSweepReadsTheWholeHostTogether(t *testing.T) {
	for _, n := range []int{1, 13, 25} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			s, _, daemon, project, target := healthWorld(t)
			s.Outcomes.followups = noFollowups{}
			for j := range n {
				name := fmt.Sprintf("app-%02d", j)
				dep, err := s.Deployments.EnsureStanding(t.Context(), project, name, target.ID, Attributes{}.WithDefaults())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Deployments.ReserveHostPort(t.Context(), project, name, name, 41000+j); err != nil {
					t.Fatal(err)
				}
				at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "first", Inputs: Inputs{Revision: "one", ImageDigest: "sha256:built"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", ImageDigest: "sha256:built", Handle: map[string]any{"container": name}}}); err != nil {
					t.Fatal(err)
				}
				// Start through the daemon contract so the container carries its real labels.
				if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"run", "-d", "--name", name, "--label", fmt.Sprintf("apptown.generation=%d", at.Generation), "app"}}); err != nil {
					t.Fatal(err)
				}
			}
			records, err := s.listRecords(t.Context(), nil)
			if err != nil || len(records) != n {
				t.Fatalf("records %d: %v", len(records), err)
			}
			before := len(daemon.Matching("inspect "))
			if err := s.ObserveRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls := len(daemon.Matching("inspect ")) - before; calls != 1 {
				t.Fatalf("host looks %d, want 1 for %d apps", calls, n)
			}
			for _, r := range records {
				if o := s.observations.read(r, s.now()); o.State != RuntimeRunning || o.Stale {
					t.Fatalf("successful reading: %+v", o)
				}
			}
			// The host returns only after the observation deadline. No app gets a
			// fresh success while another waits in the per-deployment worker queue.
			s.observationTimeout = 10 * time.Millisecond
			s.hosts.settings.Scope.Client = statusInspectClient(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
				if req.Args[0] == "inspect" {
					<-ctx.Done()
				}
				return daemon.Run(ctx, host, req)
			})
			daemon.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
				if req.Args[0] == "inspect" {
					return nil, context.DeadlineExceeded, true
				}
				return nil, nil, false
			})
			before = len(daemon.Matching("inspect "))
			if err := s.ObserveRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls := len(daemon.Matching("inspect ")) - before; calls != 1 {
				t.Fatalf("slow host looks %d, want 1", calls)
			}
			for _, r := range records {
				o := s.observations.read(r, s.now())
				status := deploymentAnswer(r.deployment, o, Diagnostics{}, nil, nil)
				if o.State != RuntimeUnknown || !o.Stale || o.ObservedAt.IsZero() || o.Err == nil || status.Word != progressLabelUnknown || status.Reason != "the host reading timed out · old reading" {
					t.Fatalf("%s after timeout: %+v status %+v", r.deployment.Name, o, status)
				}
			}
		})
	}
}
