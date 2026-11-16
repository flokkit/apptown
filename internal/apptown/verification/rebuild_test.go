package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/schema/schematest"
	"github.com/flokkit/apptown/internal/check"
)

func TestRebuildsOnlyRequestFindingsAbsentFromAUsableBundleScan(t *testing.T) {
	t.Parallel()
	f := conformance.Finding{Finding: check.Finding{Fingerprint: "F", Severity: "high"}}
	running := conformance.ScanObservation{Subject: conformance.ScanSubject{On: "running", Digest: "sha256:one"}, Scanner: "trivy", Verdict: "fail", Findings: []conformance.Finding{f}}
	bundle := func(verdict string, fs ...conformance.Finding) conformance.ScanObservation {
		return conformance.ScanObservation{Subject: conformance.ScanSubject{On: "bundle", Digest: "sha256:one"}, Scanner: "trivy", Verdict: verdict, Findings: fs}
	}
	for _, tc := range []struct {
		name   string
		scans  []conformance.ScanObservation
		ds     []conformance.Disposition
		failOn string
		want   int
	}{
		{name: "new", scans: []conformance.ScanObservation{bundle("pass")}, want: 1},
		{name: "known at build", scans: []conformance.ScanObservation{bundle("fail", f)}},
		{name: "dispositioned", ds: []conformance.Disposition{{Fingerprint: "F", Status: conformance.AcceptedRisk}}},
		{name: "below failOn", failOn: "critical"},
		{name: "no baseline", want: 1},
		{name: "error baseline is unknown", scans: []conformance.ScanObservation{bundle("error")}, want: 1},
		{name: "could not judge is unknown", scans: []conformance.ScanObservation{bundle("not applicable")}, want: 1},
		{name: "newest usable baseline", scans: []conformance.ScanObservation{bundle("error"), bundle("fail", f)}},
		{name: "newest usable supersedes earlier", scans: []conformance.ScanObservation{bundle("pass"), bundle("fail", f)}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rebuildFindings(running, tc.scans, tc.ds, tc.failOn)
			if len(got) != tc.want {
				t.Fatalf("due: %+v, want %d", got, tc.want)
			}
		})
	}
	running.Verdict = "error"
	if got := rebuildFindings(running, nil, nil, ""); len(got) != 0 {
		t.Fatalf("scanner error requested work: %+v", got)
	}
	if err := schematest.OptionsDecodedBy(RebuildKind, rebuildConfig{}); err != nil {
		t.Fatal(err)
	}
}
