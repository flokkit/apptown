package deployment

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// A landed bundle released to a Fly target goes through AppTown's own
// release door: Fly's app is made under this installation's name, each
// image is copied by digest, the init step runs before the parts, every
// part runs on a Machine carrying this installation's mark, the static
// public part is witnessed serving this release, and the release reads
// released. Taking the deployment down deletes the app it recorded.
//
// Kills: a Fly kind not registered with a driver; a release that skips the
// init step or the witness; a take-down that leaves the app.
func TestAReleaseToFlyRunsTheBundleOnMarkedMachinesAndTakeDownDeletesTheApp(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	target := p.target(t, "fly-syd")
	if target.Kind != kindFlyMachines || target.Address["org"] != "acme" {
		t.Fatalf("the Fly connector seeds its target: %+v", target)
	}
	rel, err := p.promote(t, b.ID, "cloud", target)
	events, _ := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil || rel.State != Released || !strings.Contains(asJSON(events), "the front door serves this release") || !strings.Contains(asJSON(events), "init step migrate exited 0") {
		t.Fatalf("the release: %+v %v\n%s", rel, err, asJSON(events))
	}
	dep, err := p.s.Deployments.ByName(context.Background(), p.project, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	apps := p.fly.Apps()
	if len(apps) != 1 || !strings.HasPrefix(apps[0], "at-") || !strings.HasSuffix(apps[0], "-"+deploy.HostKey(dep.ID)) {
		t.Fatalf("one app, named for this installation and deployment: %v", apps)
	}
	name := apps[0]
	app := p.fly.App(name)
	if app == nil || len(app.Machines) != 3 {
		t.Fatalf("one Machine per part in the deployment's own app: %+v", app)
	}
	for _, m := range app.Machines {
		if md := m.Metadata(); md["apptown_instance"] != testInstallationID || md["apptown_deployment"] != dep.ID.String() || md["apptown_role"] != "part" {
			t.Fatalf("metadata %v", md)
		}
	}
	// The take-down's provider step, as data protection runs it: the
	// deployment's driver tears down what its records name.
	dep, err = p.s.Deployments.ByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := p.s.providers.Driver(context.Background(), target, dep)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := p.s.Deployments.Owned(context.Background(), dep.ID)
	if err != nil || len(owned) != 1 || owned[0].Kind != "fly-app" || owned[0].Name != name {
		t.Fatalf("the app is on the deployment's owned list: %+v %v", owned, err)
	}
	if _, err := driver.Teardown(context.Background(), dep.Placement(), dep.ObservedGeneration, deploy.TakeDown{Owned: owned}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.fly.Apps(), name) {
		t.Fatalf("the take-down deletes the app: %v", p.fly.Apps())
	}
}

// testInstallationID is the promotion world's installation id.
const testInstallationID = "inst-promotion"

// What Fly cannot run is refused at the release door, before anything is
// asked of Fly: a part that keeps data in a directory.
func TestAReleaseFlyCannotRunIsRefusedBeforeFlyIsAsked(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    build: worker\n    ports: [\"8080\"]\n    dataDir: /data\n")
	b := p.land(t)
	target := p.target(t, "fly-syd")
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "cloud", target.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	before := len(p.fly.Requests())
	_, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "cloud", BundleID: b.ID})
	if !errors.Is(err, ErrRefusedContract) || !strings.Contains(err.Error(), "no persistent volumes") {
		t.Fatalf("the release: %v", err)
	}
	if after := p.fly.Requests()[before:]; len(after) != 0 {
		t.Fatalf("the refusal asked Fly: %v", after)
	}
}
