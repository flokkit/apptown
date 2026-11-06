package deployment

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestPromotionRefusesFreshStoppedSource(t *testing.T) {
	for _, takenDown := range []bool{false, true} {
		t.Run(fmt.Sprint(takenDown), func(t *testing.T) {
			t.Parallel()
			w, dep, _ := promotionFeedFixture(t)
			source, err := w.s.Deployments.ByName(t.Context(), w.project, "staging")
			if err != nil {
				t.Fatal(err)
			}
			if takenDown {
				if _, err := w.hosts.daemon("lab").Run(t.Context(), "", docker.Command{Args: []string{"rm", "-f", source.Container}}); err != nil {
					t.Fatal(err)
				}
			} else {
				w.hosts.daemon("lab").SetRunning(source.Container, false)
			}
			if got := w.s.ObserveContainerNow(t.Context(), source); got.State != ContainerStopped {
				t.Fatalf("fixture did not stop source: %+v", got)
			}
			if err := w.s.ObserveRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			summaries, err := w.s.List(t.Context(), w.project)
			if err != nil {
				t.Fatal(err)
			}
			for _, summary := range summaries {
				if summary.ID == source.ID {
					t.Logf("source runtime state: %v", summary.Observation.State)
				}
				if summary.ID == dep.ID && (!strings.Contains(summary.PromotionBlocked, "staging") || !strings.Contains(summary.PromotionBlocked, "stopped")) {
					t.Error("Promote remains enabled for a stopped source")
				}
			}
			if accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}); err == nil || !strings.Contains(err.Error(), "staging") || !strings.Contains(err.Error(), "stopped") {
				t.Fatalf("stopped source: release %+v, error %v", accepted, err)
			}
		})
	}
}

// Unknown runtime evidence does not erase the last settled bundle. Stale
// stopped evidence must not be presented as a current stop, either.
func TestPromotionWithUnknownOrStaleSourceRecordsLastSettledRelease(t *testing.T) {
	for _, state := range []string{"unknown", "stale stopped"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			w, dep, bundle := promotionFeedFixture(t)
			sourceNumber, err := w.s.Releases.LatestReleasedNumber(t.Context(), w.project, "staging")
			if err != nil || sourceNumber == 0 {
				t.Fatalf("source needs a successful release: %d, %v", sourceNumber, err)
			}
			if state == "stale stopped" {
				source, err := w.s.Deployments.ByName(t.Context(), w.project, "staging")
				if err != nil {
					t.Fatal(err)
				}
				w.hosts.daemon("lab").SetRunning(source.Container, false)
				if err := w.s.ObserveRuntime(t.Context()); err != nil {
					t.Fatal(err)
				}
				later := w.s.now().Add(observationStaleAfter + time.Second)
				w.s.setClock(func() time.Time { return later })
			}
			summaries, err := w.s.List(t.Context(), w.project)
			if err != nil {
				t.Fatal(err)
			}
			for _, summary := range summaries {
				if summary.ID == dep.ID && (summary.PromotionBlocked != "" || summary.PromotionRelease != sourceNumber) {
					t.Fatalf("promotion blocked: %s", summary.PromotionBlocked)
				}
			}
			accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name})
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.s.Releases.ByNumber(t.Context(), w.project, accepted.Number)
			if err != nil || got.BundleID != bundle.ID || got.FromDeploymentID != dep.PromotedFromID {
				t.Fatalf("release: %+v %v", got, err)
			}
			events, err := w.s.Releases.Events(t.Context(), accepted.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Text == fmt.Sprintf("promoted release %d from staging", sourceNumber) {
					return
				}
			}
			t.Fatalf("release did not record last settled source: %+v", events)
		})
	}
}
