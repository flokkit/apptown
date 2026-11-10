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
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    build: worker\n    ports: [\"8080\"]\n    health: {liveness: {http: /live}}\n")
	b := p.land(t)
	target := p.target(t, "fly-syd")
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "cloud", target.ID, Attributes{Feed: "manual", Audience: "public", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	before := len(p.fly.Requests())
	_, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "cloud", BundleID: b.ID})
	if !errors.Is(err, ErrRefusedContract) || !strings.Contains(err.Error(), "liveness") {
		t.Fatalf("the release: %v", err)
	}
	if after := p.fly.Requests()[before:]; len(after) != 0 {
		t.Fatalf("the refusal asked Fly: %v", after)
	}
}

// A Fly app answers anyone at its fly.dev address, and AppTown puts no
// sign-in in front of it: a deployment kept to a narrower audience than
// public is refused at the release door, before Fly is asked, with the
// next action.
func TestAFlyReleaseOfADeploymentNotForThePublicIsRefusedBeforeFlyIsAsked(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	target := p.target(t, "fly-syd")
	for _, audience := range []string{"team", "organisation", "partners"} {
		if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, audience, target.ID, Attributes{Feed: "manual", Audience: audience, DataClass: "synthetic"}); err != nil {
			t.Fatal(err)
		}
		before := len(p.fly.Requests())
		_, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: audience, BundleID: b.ID})
		if !errors.Is(err, ErrReleaseRefused) || !strings.Contains(err.Error(), "the "+audience+" audience") || !strings.Contains(err.Error(), "set this deployment's audience to public") {
			t.Fatalf("%s: %v", audience, err)
		}
		if after := p.fly.Requests()[before:]; len(after) != 0 {
			t.Fatalf("the refusal asked Fly: %v", after)
		}
	}
}

// A deployment that has run keeps its audience: making it public is a
// migration the owner refuses. Its refusal names a step the person can
// take instead of the edit that would be refused.
func TestAFlyRefusalForADeploymentThatHasRunNamesANewPublicDeployment(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	target := p.target(t, "fly-syd")
	team := Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"}
	dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "web", target.ID, team)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "before-the-rule", Inputs: Inputs{Revision: "earlier"}}); err != nil {
		t.Fatal(err)
	}
	public := team
	public.Audience = "public"
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "web", target.ID, public); err == nil {
		t.Fatal("an admitted deployment was made public in place; the refusal's premise is gone")
	}
	_, err = p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "web", BundleID: b.ID})
	if !errors.Is(err, ErrReleaseRefused) || strings.Contains(err.Error(), "set this deployment's audience to public") || !strings.Contains(err.Error(), "make a new public deployment of this project on fly-syd") {
		t.Fatalf("refusal: %v", err)
	}
}

// Why a Fly deployment gets no backup set before a release is said as it
// is: in this slice it holds no data AppTown backs up; it does not keep
// backups of its own. App Platform's sentence names its copy operations.
// Kills: the shared "keeps its own backups" claim, untrue for both.
func TestWhyATargetTakesNoBackupSetIsSaidAsItIs(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]string{KindS3Bucket: "holds no data AppTown backs up", KindDOApp: "copied by AppTown's copy operations"} {
		err := (&Target{Kind: kind}).HoldsBackups()
		if !errors.Is(err, ErrTargetKeepsBackups) || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "keeps its own backups") {
			t.Errorf("%s: %v, want %q", kind, err, want)
		}
	}
}

func TestAReleaseToFlyStartsCataloguePostgresBeforeInitAndWiresItsPrivateAddress(t *testing.T) {
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    build: worker\n    ports: ['8080']\nneeds: {postgres: {}}\ninit:\n  - {name: migrate, build: worker}\n")
	b := p.land(t)
	target := p.target(t, "fly-syd")
	rel, err := p.promote(t, b.ID, "cloud", target)
	if err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	events, err := p.s.Releases.Events(t.Context(), rel.ID)
	if err != nil || !strings.Contains(asJSON(events), "image registry.fly.io/") {
		t.Fatalf("need digest was not recorded on release: %v %v", events, err)
	}
	if !strings.Contains(asJSON(events), "postgres: memory 256Mi") {
		t.Fatalf("the release log does not say the size Postgres runs with:\n%s", asJSON(events))
	}
	apps := p.fly.Apps()
	if len(apps) != 1 {
		t.Fatal(apps)
	}
	app := p.fly.App(apps[0])
	var id string
	for _, m := range app.Machines {
		if m.Metadata()["apptown_role"] == "need" {
			id = m.ID
			if m.Metadata()["apptown_source_image"] != "postgres:16-alpine" {
				t.Fatal("need did not use catalogue image")
			}
			// The catalogue's default size for Postgres (256M), not the
			// default for a part that declares none.
			if guest, _ := m.Config["guest"].(map[string]any); guest["memory_mb"] != float64(256) {
				t.Fatalf("Postgres runs on %v, not its catalogue default", m.Config["guest"])
			}
		}
	}
	if id == "" {
		t.Fatal("no standing Postgres")
	}
	host := id + ".vm." + app.Name + ".internal"
	if app.Secrets["PGHOST"] != host || !strings.Contains(app.Secrets["DATABASE_URL"], "@"+host+":") {
		t.Fatal("release not wired to need")
	}
	exec, init := -1, -1
	for i, r := range p.fly.Requests() {
		if strings.HasSuffix(r.Path, "/exec") && exec < 0 {
			exec = i
		}
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/machines") && exec >= 0 && init < 0 {
			init = i
		}
	}
	if exec < 0 || init <= exec {
		t.Fatal("no readiness before init")
	}
}
