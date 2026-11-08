package diskspace

import (
	"strings"
	"testing"
)

// A damaged envelope must never become a usable set of host counters.
func TestKernelSectionsRejectMissingDuplicateAndUnknownParts(t *testing.T) {
	t.Parallel()
	valid := "== scope\nproc\n== uptime.first\n100.00 0.00\n== meminfo\nMemTotal: 1 kB\n== cpu.first\ncpu 1\n== uptime.second\n101.00 0.00\n== cpu.second\ncpu 2\n"
	if _, err := parseKernel([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scope", "uptime.first", "meminfo", "cpu.first", "uptime.second", "cpu.second"} {
		t.Run("missing "+name, func(t *testing.T) {
			start := strings.Index(valid, "== "+name+"\n")
			end := strings.Index(valid[start+3:], "== ")
			broken := valid[:start]
			if end >= 0 {
				broken += valid[start+3+end:]
			}
			if _, err := parseKernel([]byte(broken)); err == nil {
				t.Fatalf("accepted missing %s", name)
			}
		})
		t.Run("duplicate "+name, func(t *testing.T) {
			if _, err := parseKernel([]byte(valid + "== " + name + "\n")); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("duplicate %s: %v", name, err)
			}
		})
	}
	for _, tc := range []struct{ name, answer, reason string }{
		{"unknown", valid + "== surprise\n0\n", "unknown"},
		{"outside", "unlabelled\n" + valid, "outside"},
		{"bad scope", strings.Replace(valid, "\nproc\n", "\nother\n", 1), "scope"},
		{"wrong route", strings.Replace(valid, "\nproc\n", "\ncgroup\n", 1), "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseKernel([]byte(tc.answer)); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}
