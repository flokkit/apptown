package apptown

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestFindingsHTTPAnnotatesOnlyCurrentProjectExceptions(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	report := `[{"fingerprint":"known","severity":"high","identifiers":["CVE-fixture"],"summary":"known issue"},{"fingerprint":"new","severity":"low","summary":"new issue"}]`
	if _, err := a.Work.AttachArtifact(asPerson("owner"), fx.Run, workflow.ArtifactDraft{Kind: "conformance-report", Name: "review", Body: report}); err != nil {
		t.Fatal(err)
	}
	read := func(actionable, disposed int) {
		t.Helper()
		w := authzCall(h, "owner", "GET", "/api/v1/runs/"+fx.Run.String()+"/findings", "")
		var out struct {
			ProjectSlug string
			Findings    []conformance.Finding
			Summary     struct {
				Total, Dispositioned, Actionable int
				BySeverity                       map[string]int
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ProjectSlug != "game" || len(out.Findings) != 2 || out.Summary.Total != 2 || out.Summary.Actionable != actionable || out.Summary.Dispositioned != disposed || out.Summary.BySeverity["low"] != 1 {
			t.Fatalf("run findings: %d %s %v", w.Code, w.Body.String(), err)
		}
		if (out.Findings[0].Disposition != nil) != (disposed == 1) {
			t.Fatalf("annotation: %+v", out.Findings[0])
		}
	}
	read(2, 0)
	path := "/api/v1/projects/game/dispositions"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"fingerprint":"known","status":"accepted","justification":"test"}`, 403},
		{"owner", `{`, 400}, {"owner", `{"status":"accepted","justification":"test"}`, 400},
		{"owner", `{"fingerprint":"known","status":"bogus","justification":"test"}`, 400},
		{"owner", `{"fingerprint":"known","status":"accepted","justification":" "}`, 400},
		{"owner", `{"fingerprint":"known","status":"accepted","justification":"test","expiresAt":"tomorrow"}`, 400},
	} {
		w := authzCall(h, tc.who, "POST", path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("disposition refusal: %d %s", w.Code, w.Body.String())
		}
	}
	read(2, 0)
	create := func(path, body string) ids.ID {
		t.Helper()
		w := authzCall(h, "owner", "POST", path, body)
		var out struct{ ID ids.ID }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.ID.IsZero() {
			t.Fatalf("create disposition: %d %s %v", w.Code, w.Body.String(), err)
		}
		return out.ID
	}
	create("/api/v1/projects/open/dispositions", `{"fingerprint":"known","status":"accepted","justification":"only open"}`)
	read(2, 0)
	id := create(path, `{"fingerprint":"known","identifier":"CVE-fixture","status":"accepted","justification":"Upgrade scheduled"}`)
	create(path, `{"identifier":"CVE-false","status":"false_positive","justification":"Code is absent"}`)
	create(path, `{"fingerprint":"new","status":"accepted","justification":"Expired","expiresAt":"2000-01-01T00:00:00Z"}`)
	read(1, 1)
	w := authzCall(h, "owner", "GET", path, "")
	var listed struct{ Dispositions []conformance.Disposition }
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != 200 || len(listed.Dispositions) != 3 {
		t.Fatalf("all dispositions: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = authzCall(h, "owner", "GET", path+"/openvex", "")
	var vex struct {
		Statements []struct {
			Status   string
			Action   string `json:"action_statement"`
			Impact   string `json:"impact_statement"`
			Products []map[string]string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &vex); err != nil || w.Code != 200 || len(vex.Statements) != 2 {
		t.Fatalf("export: %d %s %v", w.Code, w.Body.String(), err)
	}
	states := map[string]string{}
	for _, s := range vex.Statements {
		states[s.Status] = s.Action + s.Impact
		if len(s.Products) != 1 || s.Products[0]["@id"] != "apptown:project/game" {
			t.Fatalf("export scope: %+v", s)
		}
	}
	if states["affected"] != "Upgrade scheduled" || states["not_affected"] != "Code is absent" {
		t.Fatalf("export semantics: %+v", states)
	}
	// An unlanded branch's reports never masquerade as the accepted project posture.
	w = authzCall(h, "owner", "GET", "/api/v1/projects/game/findings", "")
	var posture struct {
		Findings     []conformance.Finding
		Sources      []json.RawMessage
		Dispositions []conformance.Disposition
	}
	if err := json.Unmarshal(w.Body.Bytes(), &posture); err != nil || w.Code != 200 || posture.Findings == nil || len(posture.Findings) != 0 || posture.Sources == nil || len(posture.Sources) != 0 || len(posture.Dispositions) != 3 {
		t.Fatalf("posture: %d %s %v", w.Code, w.Body.String(), err)
	}
	revoke := fmt.Sprintf("/api/v1/dispositions/%d", id)
	if w := authzCall(h, "outsider", "DELETE", revoke, ""); w.Code != 403 {
		t.Fatalf("foreign revoke: %d %s", w.Code, w.Body.String())
	}
	read(1, 1)
	for _, code := range []int{200, 404} {
		w := authzCall(h, "owner", "DELETE", revoke, "")
		if w.Code != code {
			t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
		}
	}
	read(2, 0)
	for _, p := range []string{"/api/v1/runs/absent/findings", "/api/v1/runs/0000000f-423f-7000-8000-000000000000/findings"} {
		w := authzCall(h, "owner", "GET", p, "")
		want := 404
		if strings.Contains(p, "absent") {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("missing run: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSecurityKeepsStaleAndUnscannedNeedsUnknown(t *testing.T) {
	for _, scenario := range []string{"stale", "digest move"} {
		t.Run(scenario, func(t *testing.T) {
			moved := scenario == "digest move"
			t.Parallel()
			readings := &releaseNeedReadings{tag: "16.15", digest: "a"}
			w := newReleaseWorld(t, func(c *Config) { c.ImageResolver = readings })
			image := "postgres:16.15-alpine@sha256:" + strings.Repeat("a", 64)
			w.hosts.publicImages = map[string]bool{image: true}
			w.appYAML = releaseAppYAML + "needs: {postgres: {}}\n"
			w.mainOn(t, w.lab)
			run, step := w.landingRun(t)
			executeBootFixture(t, w.a, w.a.asEngine(t.Context()), step)
			if err := w.a.Access.SeedAdministrator(t.Context(), "boss"); err != nil {
				t.Fatal(err)
			}
			dep, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
			if err != nil {
				t.Fatal(err)
			}
			if err := w.a.Projects.SetDefaultBranch(t.Context(), dep.ProjectID, "main"); err != nil {
				t.Fatal(err)
			}
			if _, err := gitexec.Run(t.Context(), w.a.projectRepoPath("p"), "branch", "-M", "main"); err != nil {
				t.Fatal(err)
			}
			// Keep source and app coverage current so only need evidence is unknown.
			images, err := w.a.Deployment.RunningImagesIn(t.Context(), w.a.DB, dep.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := gitexec.Run(t.Context(), w.a.projectRepoPath("p"), "rev-parse", "HEAD^{tree}")
			if err != nil {
				t.Fatal(err)
			}
			if err := w.a.DB.InTx(t.Context(), func(tx *database.Tx) error {
				subjects := []conformance.ScanSubject{{On: "source", Tree: strings.TrimSpace(tree)}}
				for _, row := range images {
					for part, digest := range row.Images {
						if part != "need/postgres" {
							subjects = append(subjects, conformance.ScanSubject{On: "running", DeploymentID: dep.ID, Part: part, Digest: digest})
						}
					}
				}
				for _, subject := range subjects {
					if err := conformance.RecordScanObservationIn(t.Context(), tx, conformance.ScanObservation{ProjectID: dep.ProjectID, RunID: run.ID, StepID: step, Attempt: 1, Scanner: "trivy", ObservedAt: time.Now(), Verdict: "pass", ExpectedScanners: []string{"trivy"}, Subject: subject}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			old := time.Now()
			if !moved {
				old = old.Add(-4 * 24 * time.Hour)
			}
			observation := conformance.ScanObservation{ProjectID: dep.ProjectID, RunID: run.ID, StepID: step, Attempt: 1, Scanner: "trivy", ObservedAt: old, Verdict: "pass", ExpectedScanners: []string{"trivy"}, Subject: conformance.ScanSubject{On: "running", DeploymentID: dep.ID, Deployment: "main", Part: "need/postgres", Digest: image}}
			if err := w.a.DB.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, observation) }); err != nil {
				t.Fatal(err)
			}
			if moved {
				storage := w.a.Deployment.Deployments.StorageOf(dep)
				before, found, err := storage.Get(t.Context(), "postgres", docker.ByKey(dep.ID).NeedVolume("postgres"))
				if err != nil || !found {
					t.Fatalf("binding: %+v %v %v", before, found, err)
				}
				after := before
				after.DeclaredImage = "postgres:16.16-alpine"
				after.ImageID = "sha256:" + strings.Repeat("b", 64)
				after.RepoDigest = "postgres@sha256:" + strings.Repeat("b", 64)
				if err := storage.Move(t.Context(), before, after); err != nil {
					t.Fatal(err)
				}
			}
			coverage, err := w.a.Work.LatestMainScans(w.a.asEngine(t.Context()), dep.ProjectID, 0)
			if err != nil || coverage.Known || coverage.Stale != !moved || coverage.Reason == "" {
				t.Fatalf("coverage: %+v %v", coverage, err)
			}
			response := authzCall(w.a.Handler(), "boss", "GET", "/api/v1/projects/p/findings", "")
			var out struct {
				StatusLabel string
				Sources     []struct{ Binding, Unreadable string }
				Repo        struct{ Stale bool }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || response.Code != 200 {
				t.Fatalf("response: %d %s %v", response.Code, response.Body.String(), err)
			}

			if out.StatusLabel != "unknown" || out.Repo.Stale != coverage.Stale {
				t.Fatalf("stale or unscanned need became current success: %s", response.Body.String())
			}
			foundReason := false
			for _, source := range out.Sources {
				foundReason = foundReason || source.Unreadable == coverage.Reason
			}
			if !foundReason {
				t.Fatalf("lost coverage reason %q: %s", coverage.Reason, response.Body.String())
			}
			// Current evidence of the bound digest resolves the unknown state.
			if moved {
				observation.Subject.Digest = "postgres:16.16-alpine@sha256:" + strings.Repeat("b", 64)
			}
			observation.ObservedAt = time.Now()
			observation.Attempt++
			if err := w.a.DB.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, observation) }); err != nil {
				t.Fatal(err)
			}
			response = authzCall(w.a.Handler(), "boss", "GET", "/api/v1/projects/p/findings", "")
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || response.Code != 200 || out.StatusLabel != "passed" || out.Repo.Stale {
				t.Fatalf("current need scan: %d %s %v", response.Code, response.Body.String(), err)
			}
		})
	}
}
