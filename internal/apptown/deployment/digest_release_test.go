package deployment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// mainOf lands b on "main" on lab, as the deploy step of the landing that
// published it does (workflowexec's deploy step: RunLanding with the
// bundle), and answers main.
func (w *digestWorld) mainOf(t *testing.T, b *ReleaseBundle) *Deployment {
	t.Helper()
	out, err := w.landMain(t, context.Background(), b)
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("main on lab: %+v %v %v\nlab: %s", out.Execution, err, out.Boot.Err, strings.Join(w.hosts.commands("lab"), "\n"))
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

// landMain is the deploy step's RunLanding of b on "main" on lab, under
// the step's ctx.
func (w *digestWorld) landMain(t *testing.T, ctx context.Context, b *ReleaseBundle) (LandingResult, error) {
	t.Helper()
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "main", w.lab.ID, Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	art, cleanup, err := docker.PrepareSource(t.Context(), w.repo, w.revision, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	port, err := w.s.Deployments.StablePort(t.Context(), w.project, dep.Name, names.App())
	if err != nil {
		t.Fatal(err)
	}
	runID := b.BuiltFrom.RunID
	return w.s.RunLanding(ctx, landingClaim{w.project, runID}, LandingInput{
		Start:  Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("run-%d-deploy", runID), Inputs: Inputs{ReleaseRun: runID, Revision: w.revision, ArtifactHash: art.Hash, DockerSource: string(art.Body), BundleID: b.ID}},
		Source: *art.Source, HostPort: port, DataClass: "synthetic", Bundle: b,
	})
}

// A landing whose step is interrupted once its deploy is admitted (its run
// abandoned while the host pulls the bundle) deploys to the end and
// settles by the deploy's own outcome: main is left with no attempt that
// only a recovery observation could end.
func TestAnInterruptedLandingSettlesByItsDeploysOutcome(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	hook := func(host string) {
		if host == digestHostOf("lab") {
			interrupt()
		}
	}
	w.hosts.onPull.Store(&hook)
	out, err := w.landMain(t, ctx, b)
	w.hosts.onPull.Store(nil)
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("the interrupted landing: %+v %v %v", out.Execution, err, out.Boot.Err)
	}
	if ctx.Err() == nil {
		t.Fatal("the landing pulled nothing on lab: it was never interrupted")
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	if open, err := w.s.Outcomes.HasOpenAttempt(t.Context(), dep.ID); err != nil || open {
		t.Errorf("main's attempt is left open: %v %v", open, err)
	}
}

// removeContainer is a host losing a deployment's container.
func (w *digestWorld) removeContainer(t *testing.T, target, container string) {
	t.Helper()
	if _, err := w.hosts.daemon(target).Run(context.Background(), "", docker.Command{Args: []string{"rm", "-f", container}}); err != nil {
		t.Fatal(err)
	}
}

// A published bundle is promoted to a second Docker host, and recovered on
// the first after its container and images are gone: both pull the bundle's
// digests and build nothing.
func TestADigestBundleIsPromotedAndRecoveredWithoutABuild(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	w.mainOf(t, b)
	rel, err := w.promote(t, b.ID, "staging", w.lab2)
	if err != nil || rel.State != Released {
		t.Fatalf("promotion: %+v %v\nlab2: %s", rel, err, strings.Join(w.hosts.commands("lab2"), "\n"))
	}
	expectPulledOnly(t, w, "lab2", b)
	// The bundle's static image serves itself: the host runs it with no
	// command of its own.
	ranApp := false
	for _, line := range w.hosts.ran("lab2") {
		if strings.Contains(line, "--name "+w.names(t, "staging").App()+" ") {
			ranApp = true
			if strings.Contains(line, "caddy run") {
				t.Errorf("a self-serving static image was given a command: %s", line)
			}
		}
	}
	if !ranApp {
		t.Errorf("the static part never ran on lab2:\n%s", strings.Join(w.hosts.commands("lab2"), "\n"))
	}

	// main's container and its images are gone from lab.
	w.removeContainer(t, "lab", w.names(t, "main").App())
	w.hosts.forget("lab")
	before := len(w.hosts.pulled("lab"))
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.rebuild(t, dep)
	if err != nil || out["skipped"] != nil {
		t.Fatalf("recovery: %v %v\nlab: %s", out, err, strings.Join(w.hosts.commands("lab"), "\n"))
	}
	if pulled := w.hosts.pulled("lab")[before:]; len(pulled) != len(b.Parts)+len(b.Init) {
		t.Errorf("the recovery pulled %v", pulled)
	}
	expectPulledOnly(t, w, "lab", b)
	// The recovery runs the self-serving image as its bundle records it,
	// with no command added.
	for _, line := range w.hosts.ran("lab") {
		if strings.Contains(line, "--name "+w.names(t, "main").App()+" ") && strings.Contains(line, "caddy run") {
			t.Errorf("the recovery gave a self-serving static image a command: %s", line)
		}
	}
	after, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := w.s.Outcomes.InputsAtGeneration(context.Background(), after.ID, after.ObservedGeneration); err != nil || inputs.BundleID != b.ID || after.ObservedGeneration == dep.ObservedGeneration {
		t.Errorf("the recovered generation %d: %+v %v", after.ObservedGeneration, inputs, err)
	}
}

// A pull the registry refuses (the credential), one it cannot answer, and
// a digest it confirms it no longer has: the first two are errors, the last
// is refused with its next action, and none falls back to a build.
func TestAPullThatFailsIsAnErrorAndAMissingDigestIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		break_  func(t *testing.T, w *digestWorld, b *ReleaseBundle)
		refused bool
		want    string
	}{
		{name: "credential refused", break_: func(t *testing.T, w *digestWorld, _ *ReleaseBundle) {
			if err := w.s.env.integrations.Attachments.Attach(t.Context(), w.project, "container-registry", "missing",
				fmt.Sprintf(`{"repo":%q,"username":%q,"token":"revoked"}`, w.registry.host+"/team/p", digestRegistryUser), "test", nil); err != nil {
				t.Fatal(err)
			}
		}, want: "unauthorized"},
		{name: "registry unavailable", break_: func(_ *testing.T, w *digestWorld, _ *ReleaseBundle) { w.registry.unavailable.Store(true) }, want: "pulling"},
		{name: "digest gone", break_: func(t *testing.T, w *digestWorld, b *ReleaseBundle) {
			w.registry.remove(t, b.Parts["worker"])
		},
			refused: true, want: "land the change again"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newDigestWorld(t, "", nil)
			b := w.publish(t)
			c.break_(t, w, b)
			rel, err := w.promote(t, b.ID, "staging", w.lab2)
			if err == nil || rel.State == Released || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the promotion: %+v %v, want a failure naming %q", rel, err, c.want)
			}
			// The release's failure keeps the boot's words, not its error
			// chain: the refusal is told apart by its next action.
			if missing := strings.Contains(err.Error(), "land the change again"); missing != c.refused {
				t.Errorf("refused as missing: %v, want %v (%v)", missing, c.refused, err)
			}
			if built := w.hosts.built("lab2"); len(built) != 0 {
				t.Errorf("a failed pull fell back to a build: %v", built)
			}
			if runs := w.hosts.ran("lab2"); len(runs) != 0 {
				t.Errorf("a failed pull ran containers: %v", runs)
			}
		})
	}
}

// A digest bundle goes to a Docker host only once the host's daemon
// says it runs the bundle's platform. A promotion to a host of another
// platform, or one that names none, and a recovery on such a host, are
// refused before any pull or container: the release fails unstarted and
// the recovery admits no attempt.
//
// Kills: promotion or recovery of a digest bundle that never asks the
// target's platform (step 3 asked only at landing).
func TestPromotionAndRecoveryRefuseAHostOfAnotherOrUnknownPlatform(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"linux aarch64", "linux "} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			w := newDigestWorld(t, "", nil)
			b := w.publish(t)
			w.mainOf(t, b)
			dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "staging", w.lab2.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name, BundleID: b.ID})
			if err != nil {
				t.Fatal(err)
			}
			w.hosts.platform = platform
			err = w.s.ExecuteRelease(context.Background(), accepted.ID)
			rel, readErr := w.s.Releases.Last(context.Background(), w.project, "staging")
			if readErr != nil {
				t.Fatal(readErr)
			}
			pulls, runs := w.hosts.pulled("lab2"), w.hosts.ran("lab2")
			if !errors.Is(err, ErrReleaseRefused) || rel.State != ReleaseFailed || len(pulls) != 0 || len(runs) != 0 {
				t.Fatalf("bundle platform %s, target platform %q: err=%v state=%s pulls=%d runs=%d; want refusal before effects", b.BuiltFrom.Platform, platform, err, rel.State, len(pulls), len(runs))
			}

			// main's container and its images are gone from lab, whose
			// daemon now answers the same platform.
			w.removeContainer(t, "lab", w.names(t, "main").App())
			w.hosts.forget("lab")
			before := len(w.hosts.pulled("lab"))
			main, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.rebuild(t, main)
			after, readErr := w.s.Deployments.ByName(t.Context(), w.project, "main")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !errors.Is(err, ErrReleaseRefused) || len(w.hosts.pulled("lab")) != before || after.DesiredGeneration != main.DesiredGeneration {
				t.Fatalf("recovery on a host of platform %q: %v, pulls %v, generation %d -> %d", platform, err, w.hosts.pulled("lab")[before:], main.DesiredGeneration, after.DesiredGeneration)
			}
		})
	}
}

// Readable tags beside the digests: a landing's images are published as
// change-<change>-<image>, and a release that goes live adds
// release-<n>-<image> to the same manifests in the project's registry. A
// tag is not evidence: a tag the registry refuses leaves the release
// released, with the refusal in its log.
//
// Kills: a release tag written as another image (another digest), or never
// written; a tag failure failing the release.
func TestAReleaseTagsItsDigestsAndATagFailureNeverFailsIt(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	digest := func(ref string) string { _, d, _ := strings.Cut(ref, "@"); return d }
	tagged := func(tag string) string { return w.registry.digestOf(t, w.registry.host+"/team/p:"+tag) }
	if got := tagged(fmt.Sprintf("change-%d-part-web", digestChange)); got != digest(b.Parts["web"]) {
		t.Errorf("the landing's web image is tagged %q, want %s", got, digest(b.Parts["web"]))
	}

	rel, err := w.promote(t, b.ID, "staging", w.lab2)
	if err != nil || rel.State != Released {
		t.Fatalf("promotion: %+v %v", rel, err)
	}
	for tag, ref := range map[string]string{"part-web": b.Parts["web"], "part-cache": b.Parts["cache"], "init-migrate": b.Init["migrate"]} {
		if got := tagged(fmt.Sprintf("release-%d-%s", rel.Number, tag)); got != digest(ref) {
			t.Errorf("release #%d's %s is tagged %q, want %s", rel.Number, tag, got, digest(ref))
		}
	}

	refuseTags := func(req *http.Request) {
		if req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/manifests/release-") {
			w.registry.refusing.Store(true)
		}
	}
	w.registry.onRequest.Store(&refuseTags)
	again, err := w.promote(t, b.ID, "staging", w.lab2)
	w.registry.onRequest.Store(nil)
	w.registry.refusing.Store(false)
	if err != nil || again.State != Released {
		t.Fatalf("a release whose tags the registry refused: %+v %v", again, err)
	}
	if got := tagged(fmt.Sprintf("release-%d-part-web", again.Number)); got != "" {
		t.Errorf("a refused tag exists: %s", got)
	}
	events, err := w.s.Releases.Events(context.Background(), again.ID)
	if err != nil || !strings.Contains(fmt.Sprint(events), "could not tag") {
		t.Errorf("the release's log does not say the tag failed: %+v %v", events, err)
	}
}

// A landing-fed deployment without recorded inputs cannot establish which
// bundle it runs. Historical release metadata does not authorize a manual
// redeploy, even when the requested bundle matches that metadata.
func TestALegacyLandingFedDeploymentCannotInferItsRunningBundle(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	w.mainOf(t, b)
	first, err := w.promote(t, b.ID, "staging", w.lab2)
	if err != nil || first.State != Released {
		t.Fatalf("first release: %+v %v", first, err)
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "staging")
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: historical generations can lack inputs and release generation metadata; no operation writes that shape now.
	mustExec(t, w.db, `DELETE FROM deployment_attempt_inputs USING deployment_attempts a WHERE a.id=attempt_id AND a.deployment_id=$1`, dep.ID)
	// sql-guarantee: preserve the historical release metadata while removing its generation association.
	mustExec(t, w.db, `UPDATE releases SET generation=0, commit_sha='' WHERE deployment_id=$1`, dep.ID)
	mutateStoredDeployment(t, w.db, w.project, "staging", func(r *deploymentsql.Deployment) { r.Feed = "landing" })

	_, err = w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: "staging", BundleID: b.ID})
	if !errors.Is(err, errFedByLanding) {
		t.Fatalf("unknown running bundle must refuse manual release: %v", err)
	}
	last, err := w.s.Releases.Last(t.Context(), w.project, "staging")
	if err != nil || last.ID != first.ID || last.State != Released || last.Generation != 0 {
		t.Fatalf("refusal must preserve historical release: %+v %v", last, err)
	}
	after, err := w.s.Deployments.ByName(t.Context(), w.project, "staging")
	if err != nil || after.ObservedGeneration != dep.ObservedGeneration || after.DesiredGeneration != dep.DesiredGeneration {
		t.Fatalf("refusal must preserve deployment generation: %+v %v", after, err)
	}
}

// A release to a Docker host short of disk is refused before anything is
// pulled, removed or started there: it does not become a successful
// deploy, the deployment keeps serving its generation, and the refusal
// says what is free. Once space is freed the same release goes through.
//
// Kills: AdmitDisk removed from Boot (the refused release pulls and
// replaces the serving container).
func TestAReleaseToAHostShortOfDiskChangesNothingThere(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("the first release: %+v %v", rel, err)
	}
	serving, err := w.s.Deployments.ByName(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	daemon := w.hosts.daemon("lab")
	daemon.SetDisk(100<<30, 1<<30) // 1 GiB free of 100: below the 5 GiB floor
	before := len(w.hosts.commands("lab"))
	rel, err := w.promote(t, b.ID, "prod", w.lab)
	if rel.State == Released || err == nil || !strings.Contains(err.Error(), "1.0 GiB free of 100.0 GiB") {
		t.Fatalf("a release to a host short of disk: %s %v", rel.State, err)
	}
	for _, c := range w.hosts.commands("lab")[before:] {
		if !strings.HasPrefix(c, "info") && !daemon.IsDiskRead(strings.Fields(c)) && !strings.HasPrefix(c, "inspect") && !strings.HasPrefix(c, "ps") {
			t.Errorf("the host was asked %q after the refusal", c)
		}
	}
	after, err := w.s.Deployments.ByName(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if after.ObservedGeneration != serving.ObservedGeneration {
		t.Errorf("the serving generation moved from %d to %d", serving.ObservedGeneration, after.ObservedGeneration)
	}
	if !(docker.Scope{Client: daemon}).Running(t.Context(), w.names(t, "prod").App()) {
		t.Error("the serving container is not running")
	}
	daemon.SetDisk(100<<30, 50<<30)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("the release once space was freed: %+v %v", rel, err)
	}
}
