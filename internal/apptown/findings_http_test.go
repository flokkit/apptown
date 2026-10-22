package apptown

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
