package deployment

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// plantBeforeKey makes a deployment one made before its names were by key.
func plantBeforeKey(t *testing.T, w *digestWorld, id ids.ID) {
	t.Helper()
	// No operation makes a deployment on the names it was made with now;
	// migration 162 keeps the prefix of those that exist.
	// sql-guarantee: a deployment made before its names were by key.
	mustExec(t, w.db, `UPDATE deployments SET host_prefix = 'forge-', host_label = '7' WHERE id = ?`, id)
}

// Two deployments whose ids end alike would share every name on a host,
// so the second is refused where it is recorded.
//
// Kills: the unique index on the id's last 12 hex digits dropped from
// migration 162 or the schema.
func TestTwoDeploymentsNeverShareAKey(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	// Ids are minted at random, so only planted ones collide.
	// sql-guarantee: two deployments whose ids end alike.
	if _, err := w.db.Exec(`INSERT INTO deployments (id, project_id, name, updated_at) VALUES ('01a15e00-0000-7000-8000-0000000c0ffe', ?, 'first', '2026-10-22T12:00:00Z')`, w.project); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the second of them, which the index refuses.
	_, err := w.db.Exec(`INSERT INTO deployments (id, project_id, name, updated_at) VALUES ('01a15e99-0000-7000-8000-0000000c0ffe', ?, 'second', '2026-10-22T12:00:00Z')`, w.project)
	if err == nil || !strings.Contains(err.Error(), "deployments_host_key") {
		t.Fatalf("a second deployment with the same key was recorded: %v", err)
	}
}

// The move is asked for one deployment at a time, and only for a standing
// Docker deployment still on the names it was made with that runs a
// release: it is then a release of the bundle it runs, recorded as a move.
//
// Kills: refusesMove's prefix or target check dropped; the running-bundle
// check dropped (a never-released deployment moved with nothing to find);
// MoveNames not written to the request.
func TestAMoveIsAReleaseOfWhatALegacyDockerDeploymentRuns(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	first := w.publish(t)
	if rel, err := w.promote(t, first.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	ask := func(name string) (AcceptedRelease, error) {
		return w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: name, MoveNames: true})
	}
	if _, err := ask("prod"); err == nil || !strings.Contains(err.Error(), "already named by its key") {
		t.Fatalf("a deployment named by its key was moved: %v", err)
	}

	prod := mustDeployment(t, w, "prod")
	plantBeforeKey(t, w, prod.ID)
	accepted, err := ask("prod")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := w.s.Releases.ByNumber(context.Background(), w.project, accepted.Number)
	if err != nil || rel.BundleID != first.ID {
		t.Fatalf("the move releases %v, not the running bundle %v: %v", rel.BundleID, first.ID, err)
	}

	// Never released: a failed first release may have left data, so only
	// a release of what runs may move it.
	stage, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "stage", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	plantBeforeKey(t, w, stage.ID)
	if _, err := ask("stage"); err == nil || !strings.Contains(err.Error(), "release it first, then move it") {
		t.Fatalf("a deployment with nothing released was moved: %v", err)
	}
}

// Kills: a Portainer or a non-Docker deployment admitted to the move.
func TestOnlyADockerDeploymentIsMoved(t *testing.T) {
	t.Parallel()
	dep := &Deployment{Name: "lan", Kind: Standing, HostPrefix: "forge-"}
	for kind, want := range map[string]string{KindPortainer: "Take it down and release it again", "do-app": "names nothing on a Docker host", KindDockerHost: ""} {
		got := refusesMove(dep, &Target{Kind: kind, Name: "t"})
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", kind, got, want)
		}
	}
}

// The switch is one transaction: the deployment is named by key with its
// UUID as its label, what was recorded under its old names on the host is
// forgotten (the old volumes stay on the host, outside its records), its
// build images stay recorded, and the copies are recorded.
//
// Kills: host_label left (the new containers' identity would not match
// the record); image rows forgotten (take-down would leave the build
// images); old volume rows kept (take-down would delete the kept copies);
// the copies not recorded (ensureVolume never records an existing volume).
func TestSwitchingToKeyNamesRecordsTheMove(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	if rel, err := w.promote(t, w.publish(t).ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	prod := mustDeployment(t, w, "prod")
	plantBeforeKey(t, w, prod.ID)
	owner := w.s.Deployments.OwnerOf(prod)
	for _, o := range []deploy.Owned{{Kind: "volume", ID: "forge-need-env-p-prod-postgres", Data: true, Need: "postgres"}, {Kind: "network", ID: "forge-net-env-p-prod"},
		{Kind: "image", ID: "forge-preview-build-forge-env-p-prod-g1-web"}} {
		if err := owner.Created(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	copied := deploy.Owned{Kind: "volume", ID: "apptown-need-dep-" + deploy.HostKey(prod.ID) + "-postgres", Data: true, Need: "postgres"}
	if err := w.s.Deployments.SwitchToKeyNames(t.Context(), prod.ID, []deploy.Owned{copied}); err != nil {
		t.Fatal(err)
	}
	after := mustDeployment(t, w, "prod")
	if after.HostPrefix != "" || after.Label() != deploy.HostLabel(prod.ID.String()) {
		t.Fatalf("not named by key: prefix %q label %q", after.HostPrefix, after.Label())
	}
	owned, err := w.s.Deployments.Owned(t.Context(), prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, o := range owned {
		switch o.Kind {
		case "image":
			kinds = append(kinds, "image")
		case "volume":
			if o.ID == copied.ID && o.Need == "postgres" && o.Data {
				kinds = append(kinds, "copy")
			} else {
				t.Errorf("an old name is still recorded: %+v", o)
			}
		default:
			t.Errorf("an old name is still recorded: %+v", o)
		}
	}
	if !slices.Contains(kinds, "image") || !slices.Contains(kinds, "copy") {
		t.Fatalf("after the switch it records %+v", owned)
	}
	if err := w.s.Deployments.SwitchToKeyNames(t.Context(), prod.ID, nil); err == nil || !strings.Contains(err.Error(), "no longer on the names it was made with") {
		t.Fatalf("a second switch: %v", err)
	}
}

// A move is a release: its attempt boots the deployment under its names by
// key and switches its record, and the deployment records the new
// container. One whose deployment moved on before it ran (another release
// admitted meanwhile) moves nothing: its record keeps the old names and its
// host is not touched.
//
// Kills: a move done before the release's admission, or one that does not
// reach the driver (moveDriver dropped from execute).
func TestAMoveRunsInItsReleaseAndAStaleOneMovesNothing(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	first := w.publish(t)
	if rel, err := w.promote(t, first.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	prod := mustDeployment(t, w, "prod")
	plantBeforeKey(t, w, prod.ID)

	// Stale: a landing admits another generation of prod first.
	stale, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "prod", MoveNames: true})
	if err != nil {
		t.Fatal(err)
	}
	// A manual release cannot be asked while the move waits.
	// sql-guarantee: a landing's admission moves the generation on.
	mustExec(t, w.db, `UPDATE deployments SET desired_generation = desired_generation + 1 WHERE id = ?`, prod.ID)
	before := len(w.hosts.daemon(w.lab.Name).Commands())
	if err := w.s.ExecuteRelease(context.Background(), stale.ID); err == nil {
		t.Fatal("a move whose deployment moved on ran")
	}
	if after := mustDeployment(t, w, "prod"); after.HostPrefix != "forge-" {
		t.Fatalf("a stale move switched the record: %q", after.HostPrefix)
	}
	if got := w.hosts.daemon(w.lab.Name).Commands()[before:]; len(got) != 0 {
		t.Fatalf("a stale move touched the host: %v", got)
	}

	// Current: it runs, inside its attempt.
	// sql-guarantee: the landing's generation taken back, as if it never came.
	mustExec(t, w.db, `UPDATE deployments SET desired_generation = desired_generation - 1 WHERE id = ?`, prod.ID)
	accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "prod", MoveNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	after := mustDeployment(t, w, "prod")
	if after.HostPrefix != "" || after.Container != docker.ByKey(prod.ID).App() {
		t.Fatalf("after the move: prefix %q, container %q", after.HostPrefix, after.Container)
	}
}

// Main is fed by landing, so no one deploys it by hand; its move is still
// a release of the bundle it runs, and so is admitted (live's apps are all
// main).
//
// Kills: the move's running bundle chosen after the landing-fed refusal.
func TestALandingFedMainIsMoved(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	main := w.mainOf(t, b)
	plantBeforeKey(t, w, main.ID)
	accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "main", MoveNames: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	if after := mustDeployment(t, w, "main"); after.HostPrefix != "" || after.Container != docker.ByKey(main.ID).App() {
		t.Fatalf("main after its move: prefix %q container %q", after.HostPrefix, after.Container)
	}
}
