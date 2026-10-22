package check_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// Fixtures are trimmed real output: osv-scanner v2.2.4 against a Go module,
// gitleaks v8.30.0 SARIF with one detected PAT.

const osvFixture = `{
  "results": [{
    "source": {"path": "/work/go.mod", "type": "lockfile"},
    "packages": [{
      "package": {"name": "stdlib", "version": "1.25.0", "ecosystem": "Go"},
      "vulnerabilities": [
        {"id": "GO-2025-3955", "aliases": ["BIT-golang-2025-47910", "CVE-2025-47910"],
         "summary": "CrossOriginProtection insecure bypass",
         "database_specific": {"url": "https://pkg.go.dev/vuln/GO-2025-3955"},
         "affected": [{"ranges": [{"type": "SEMVER", "events": [{"introduced": "1.25.0"}, {"fixed": "1.25.1"}]}]}]},
        {"id": "GO-2025-4006", "aliases": ["CVE-2025-61725"],
         "summary": "Excessive CPU in ParseAddress",
         "database_specific": {"url": "https://pkg.go.dev/vuln/GO-2025-4006"},
         "affected": [{"ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.24.8"}, {"introduced": "1.25.0"}, {"fixed": "1.25.2"}]}]}]}
      ],
      "groups": [
        {"ids": ["GO-2025-3955"], "max_severity": "", "experimental_analysis": {"GO-2025-3955": {"called": false}}},
        {"ids": ["GO-2025-4006"], "max_severity": "7.5", "experimental_analysis": {"GO-2025-4006": {"called": true}}}
      ]
    }]
  }]
}`

const sarifFixture = `{
  "runs": [{
    "tool": {"driver": {"name": "gitleaks", "semanticVersion": "v8.0.0"}},
    "results": [{
      "ruleId": "github-pat",
      "message": {"text": "github-pat has detected secret for file /work/leaky.txt."},
      "locations": [{"physicalLocation": {
        "artifactLocation": {"uri": "/work/leaky.txt"},
        "region": {"startLine": 1}}}],
      "partialFingerprints": {"commitSha": "", "email": ""}
    }]
  }]
}`

func TestNormalizeOSV(t *testing.T) {
	t.Parallel()
	fs, err := check.Normalize("osv", []byte(osvFixture), "osv", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings, got %d", len(fs))
	}
	f := fs[0]
	if f.Fingerprint != "vuln|GO-2025-3955|stdlib" {
		t.Errorf("fingerprint: %q", f.Fingerprint)
	}
	if f.Severity != "unknown" {
		t.Errorf("unscored advisory should be unknown, got %q", f.Severity)
	}
	if f.Package == nil || f.Package.FixedIn != "1.25.1" {
		t.Errorf("fixedIn: %+v", f.Package)
	}
	if len(f.Identifiers) != 3 || f.Identifiers[2] != "CVE-2025-47910" {
		t.Errorf("identifiers: %v", f.Identifiers)
	}
	if fs[1].Severity != "high" { // group max_severity 7.5
		t.Errorf("scored 7.5 should be high, got %q", fs[1].Severity)
	}
	if f.Reached == nil || *f.Reached {
		t.Errorf("call analysis called=false should yield Reached=false, got %v", f.Reached)
	}
	if fs[1].Reached == nil || !*fs[1].Reached {
		t.Errorf("called=true should yield Reached=true")
	}
	// Multi-branch advisory at stdlib 1.25.0: the relevant fix is 1.25.2 on
	// this branch, NOT the 1.24.8 backport.
	if fs[1].Package.FixedIn != "1.25.2" {
		t.Errorf("fixedIn should pick the branch-relevant fix, got %q", fs[1].Package.FixedIn)
	}
}

func TestNormalizeSARIF(t *testing.T) {
	t.Parallel()
	fs, err := check.Normalize("sarif", []byte(sarifFixture), "gitleaks", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	f := fs[0]
	if f.Severity != "high" {
		t.Errorf("secret without level should default high, got %q", f.Severity)
	}
	if f.Location == nil || f.Location.File != "leaky.txt" || f.Location.Line != 1 {
		t.Errorf("location: %+v", f.Location)
	}
	if f.Fingerprint != "secret|github-pat|leaky.txt|1" {
		t.Errorf("fingerprint: %q (empty partialFingerprints must not be used)", f.Fingerprint)
	}
}

// semgrep emits severity on the RULE (defaultConfiguration.level +
// properties.security-severity), never on the result, and marks non-findings
// with kind other than "fail". Exercise both: rule-inherited severity, the
// CVSS bucket, and a kind:"pass" result that must be dropped.
const sarifRuleSeverityFixture = `{
  "runs": [{
    "tool": {"driver": {"name": "semgrep", "rules": [
      {"id": "missing-user", "defaultConfiguration": {"level": "error"}},
      {"id": "unscoped-find", "defaultConfiguration": {"level": "warning"}},
      {"id": "hardcoded-cvss", "defaultConfiguration": {"level": "warning"},
        "properties": {"security-severity": "9.1"}},
      {"id": "clean-check", "defaultConfiguration": {"level": "note"}}
    ]}},
    "results": [
      {"ruleId": "missing-user", "message": {"text": "runs as root"},
        "locations": [{"physicalLocation": {"artifactLocation": {"uri": "/work/Dockerfile"}, "region": {"startLine": 3}}}]},
      {"ruleId": "unscoped-find", "message": {"text": "unscoped find"},
        "locations": [{"physicalLocation": {"artifactLocation": {"uri": "/work/app.rb"}, "region": {"startLine": 9}}}]},
      {"ruleId": "hardcoded-cvss", "message": {"text": "scored high"},
        "locations": [{"physicalLocation": {"artifactLocation": {"uri": "/work/app.rb"}, "region": {"startLine": 20}}}]},
      {"ruleId": "clean-check", "kind": "pass", "message": {"text": "rule matched, nothing wrong"},
        "locations": [{"physicalLocation": {"artifactLocation": {"uri": "/work/app.rb"}, "region": {"startLine": 30}}}]}
    ]
  }]
}`

func TestNormalizeSARIFRuleSeverityAndKind(t *testing.T) {
	t.Parallel()
	fs, err := check.Normalize("sarif", []byte(sarifRuleSeverityFixture), "semgrep", "sast")
	if err != nil {
		t.Fatal(err)
	}
	// The kind:"pass" result is not a finding — three remain, none "unknown".
	if len(fs) != 3 {
		t.Fatalf("want 3 findings (kind:pass dropped), got %d", len(fs))
	}
	sev := map[string]string{}
	for _, f := range fs {
		sev[f.Identifiers[0]] = f.Severity
	}
	if sev["missing-user"] != "high" { // rule level "error" → high
		t.Errorf("rule-level error should be high, got %q", sev["missing-user"])
	}
	if sev["unscoped-find"] != "medium" { // rule level "warning" → medium
		t.Errorf("rule-level warning should be medium, got %q", sev["unscoped-find"])
	}
	if sev["hardcoded-cvss"] != "critical" { // security-severity 9.1 → critical
		t.Errorf("security-severity 9.1 should be critical, got %q", sev["hardcoded-cvss"])
	}
	if _, ok := sev["clean-check"]; ok {
		t.Errorf("a kind:pass result must not become a finding")
	}
}

func TestSeverityRank(t *testing.T) {
	t.Parallel()
	if check.SeverityRank("unknown") != check.SeverityRank("high") {
		t.Error("unknown must rank as high — unscored is not shippable-by-default")
	}
	if !(check.SeverityRank("critical") > check.SeverityRank("high") && check.SeverityRank("high") > check.SeverityRank("medium") &&
		check.SeverityRank("medium") > check.SeverityRank("low") && check.SeverityRank("low") > check.SeverityRank("note")) {
		t.Error("rank ordering broken")
	}
	// note is the default a check declaration gets; it once ranked as high,
	// so a note-level check blocked every landing gated at high.
	if check.SeverityRank("note") >= check.SeverityRank("high") {
		t.Error("a note must not rank as high")
	}
}
