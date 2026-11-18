package conformance

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// The clock runs from the first scan of any kind that found a vulnerability to
// the first usable scan of the same running part, by the same scanner, without
// it after its latest sighting there.
func TestPatchTimesMeasureFirstSightingToTheRunningAppWithoutIt(t *testing.T) {
	db := testDB(t)
	project := createTestProject(t, db, "patched", "Patched", "main")
	other := createTestProject(t, db, "elsewhere", "Elsewhere", "main")
	deployment := ids.New(time.Now())
	day := func(n int) time.Time {
		return time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC).Add(time.Duration(n) * 24 * time.Hour)
	}
	observe := func(p ids.ID, at time.Time, on, part, scanner, verdict string, fingerprints ...string) {
		t.Helper()
		o := ScanObservation{ProjectID: p, RunID: ids.New(at), StepID: ids.New(at), Attempt: 1, Scanner: scanner, ObservedAt: at, Verdict: verdict,
			Subject: ScanSubject{On: on, Commit: "abc"}}
		if on == "running" {
			o.Subject = ScanSubject{On: on, DeploymentID: deployment, Deployment: "main", Part: part, Digest: "sha256:" + at.Format("20060102")}
		}
		for _, fp := range fingerprints {
			o.Findings = append(o.Findings, Finding{Finding: check.Finding{Fingerprint: fp, Kind: "vuln", Severity: "high"}})
		}
		// A repeated fingerprint in one receipt counts once.
		if len(fingerprints) > 0 {
			o.Findings = append(o.Findings, Finding{Finding: check.Finding{Fingerprint: fingerprints[0], Kind: "vuln", Severity: "high"}})
		}
		if err := db.InTx(t.Context(), func(tx *database.Tx) error { return RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
			t.Fatal(err)
		}
	}
	// "early": the source scan found it a day before the running app did;
	// gone from web on day 3 (an errored day-2 scan proves nothing).
	observe(project, day(0), "source", "", "osv", "fail", "vuln|early|pkg")
	observe(project, day(1), "running", "web", "trivy", "fail", "vuln|early|pkg", "vuln|back|pkg", "vuln|stays|pkg")
	observe(project, day(1), "running", "worker", "trivy", "pass")
	observe(project, day(2), "running", "web", "trivy", "error")
	observe(project, day(2), "running", "worker", "trivy", "pass")
	// "back": gone on day 3, found again on day 4, gone on day 5.
	observe(project, day(3), "running", "web", "trivy", "fail", "vuln|stays|pkg")
	observe(project, day(4), "running", "web", "trivy", "fail", "vuln|back|pkg", "vuln|stays|pkg")
	observe(project, day(5), "running", "web", "trivy", "fail", "vuln|stays|pkg")
	// Only in source: not in the running app, so not measured.
	observe(project, day(1), "source", "", "osv", "fail", "vuln|source-only|pkg")
	// Another project's patch is not this project's.
	observe(other, day(1), "running", "web", "trivy", "fail", "vuln|early|pkg")
	observe(other, day(2), "running", "web", "trivy", "pass")

	units, err := PatchTimesIn(t.Context(), db, []ids.ID{project}, day(0))
	if err != nil {
		t.Fatal(err)
	}
	type unit struct {
		first, patched time.Time
		ok             bool
	}
	got := []unit{}
	for _, u := range units {
		if u.ProjectID != project {
			t.Fatalf("unrequested project: %+v", u)
		}
		got = append(got, unit{u.FirstSeen, u.PatchedAt, u.Patched})
	}
	want := []unit{{day(0), day(3), true}, {day(1), day(5), true}, {day(1), time.Time{}, false}}
	if len(got) != len(want) {
		t.Fatalf("units: %+v", units)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g.first.Equal(w.first) && g.ok == w.ok && (!w.ok || g.patched.Equal(w.patched))
		}
		if !found {
			t.Fatalf("missing %+v in %+v", w, got)
		}
	}

	// A period starting after a patch leaves it out; a patch in the period and
	// a still-open unit seen in the period stay.
	later, err := PatchTimesIn(t.Context(), db, []ids.ID{project}, day(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 2 {
		t.Fatalf("period: %+v", later)
	}
	none, err := PatchTimesIn(t.Context(), db, nil, day(0))
	if err != nil || len(none) != 0 {
		t.Fatalf("no projects: %+v %v", none, err)
	}
}

// The recurrence rule: a vulnerability that comes back and is never seen gone
// again is open, however often it was gone before.
func TestPatchTimesKeepAReturnedVulnerabilityOpen(t *testing.T) {
	db := testDB(t)
	project := createTestProject(t, db, "returned", "Returned", "main")
	deployment := ids.New(time.Now())
	at := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	for i, present := range []bool{true, false, true} {
		o := ScanObservation{ProjectID: project, RunID: ids.New(at), StepID: ids.New(at), Attempt: 1, Scanner: "trivy", ObservedAt: at.Add(time.Duration(i) * 24 * time.Hour), Verdict: "pass",
			Subject: ScanSubject{On: "running", DeploymentID: deployment, Part: "web", Digest: "sha256:x"}}
		if present {
			o.Verdict = "fail"
			o.Findings = []Finding{{Finding: check.Finding{Fingerprint: "vuln|back|pkg", Kind: "vuln"}}}
		}
		if err := db.InTx(t.Context(), func(tx *database.Tx) error { return RecordScanObservationIn(t.Context(), tx, o) }); err != nil {
			t.Fatal(err)
		}
	}
	units, err := PatchTimesIn(t.Context(), db, []ids.ID{project}, at)
	if err != nil || len(units) != 1 || units[0].Patched {
		t.Fatalf("returned vulnerability: %+v %v", units, err)
	}
}
