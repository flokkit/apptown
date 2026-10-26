package routinechange

import (
	"strings"
	"testing"
)

func TestAdvisoryRequiresAnExactFixedBoundaryAcrossAllAffectedEntries(t *testing.T) {
	base := `{"id":"GHSA-example","modified":"2026-10-01T00:00:00Z","affected":[{"package":{"ecosystem":"npm","name":"example"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.0.1"}]}]}]}`
	fix, err := NPMAdvisoryFix([]byte(base), "example", "1.0.0", "1.0.1")
	if err != nil || fix.ID != "GHSA-example" {
		t.Fatalf("exact fix: %+v %v", fix, err)
	}
	cases := map[string]string{
		"withdrawn":                 strings.Replace(base, `"modified":`, `"withdrawn":"2026-10-02T00:00:00Z","modified":`, 1),
		"wrong package":             strings.Replace(base, `"name":"example"`, `"name":"other"`, 1),
		"wrong ecosystem":           strings.Replace(base, `"ecosystem":"npm"`, `"ecosystem":"Go"`, 1),
		"not fixed":                 strings.Replace(base, `"fixed":"1.0.1"`, `"fixed":"1.0.2"`, 1),
		"unsupported boundary":      strings.Replace(base, `"fixed":`, `"last_affected":`, 1),
		"open ended later interval": strings.Replace(base, `{"fixed":"1.0.1"}`, `{"fixed":"1.0.1"},{"introduced":"2.0.0"}`, 1),
		"unknown range":             strings.Replace(base, `"SEMVER"`, `"ECOSYSTEM"`, 1),
		"duplicate key":             strings.Replace(base, `"id":"GHSA-example"`, `"id":"GHSA-example","id":"different"`, 1),
		"explicit candidate":        strings.Replace(base, `"ranges":`, `"versions":["1.0.1"],"ranges":`, 1),
		"second affected entry":     strings.TrimSuffix(base, `}]}`) + `},{"package":{"ecosystem":"npm","name":"example"},"versions":["1.0.1"]}]}`,
		"second unsupported entry":  strings.TrimSuffix(base, `}]}`) + `},{"package":{"ecosystem":"npm","name":"example"},"ranges":[{"type":"GIT","events":[{"introduced":"0"}]}]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NPMAdvisoryFix([]byte(raw), "example", "1.0.0", "1.0.1"); err == nil {
				t.Fatal("unsupported fix claim accepted")
			}
		})
	}
	if _, err := NPMAdvisoryFix([]byte(base), "example", "1.0.1", "1.0.2"); err == nil {
		t.Fatal("unaffected old version counted as fixed")
	}
}
