package verification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/routinechange"
)

func routineFixture(version string) routinechange.NPMSnapshot {
	return routinechange.NPMSnapshot{Manifest: []byte(fmt.Sprintf(`{"name":"app","dependencies":{"example":"%s"}}`, version)), Lock: []byte(fmt.Sprintf(`{"name":"app","lockfileVersion":3,"packages":{"":{"name":"app","dependencies":{"example":"%s"}},"node_modules/example":{"version":"%s","resolved":"https://registry.npmjs.org/example/-/example-%s.tgz","integrity":"sha512-%s"}}}`, version, version, version, base64.StdEncoding.EncodeToString(make([]byte, 64))))}
}
func routineReport(before, after routinechange.NPMSnapshot) npmObservation {
	side := func(s routinechange.NPMSnapshot, version string) npmSide {
		return npmSide{Input: []string{digestNPM(s.Manifest), digestNPM(s.Lock)}, Output: []string{digestNPM(s.Manifest), digestNPM(s.Lock)}, Installed: map[string]string{"node_modules/example": fmt.Sprintf(`{"name":"example","version":"%s"}`, version)}}
	}
	return npmObservation{Node: "v24.0.0", NPM: "11.0.0", Platform: "linux/arm64", Before: side(before, "1.0.0"), After: side(after, "1.0.1")}
}

func TestRoutineValidationRequiresExactInputImageAndInstalledEvidence(t *testing.T) {
	before, after := routineFixture("1.0.0"), routineFixture("1.0.1")
	candidate, err := routinechange.CompareNPM(before, after, "patch")
	if err != nil {
		t.Fatal(err)
	}
	image := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name string
		edit func(*npmObservation, *execution.Outcome)
	}{
		{"complete", func(*npmObservation, *execution.Outcome) {}},
		{"changed input", func(r *npmObservation, _ *execution.Outcome) { r.After.Input[0] = "other" }},
		{"changed output", func(r *npmObservation, _ *execution.Outcome) { r.After.Output[1] = "other" }},
		{"wrong installed version", func(r *npmObservation, _ *execution.Outcome) {
			r.After.Installed["node_modules/example"] = `{"name":"example","version":"9.0.0"}`
		}},
		{"hidden script", func(r *npmObservation, _ *execution.Outcome) {
			r.After.Installed["node_modules/example"] = `{"name":"example","version":"1.0.1","scripts":{"postinstall":"touch /canary"}}`
		}},
		{"partial inventory", func(r *npmObservation, _ *execution.Outcome) { r.Before.Installed = nil }},
		{"timeout", func(_ *npmObservation, o *execution.Outcome) { o.Termination = execution.TimedOut }},
		{"cleanup failure", func(_ *npmObservation, o *execution.Outcome) { o.CleanupErr = errors.New("container remains") }},
		{"worker cleanup failure", func(_ *npmObservation, o *execution.Outcome) { o.RunErr = errors.New("worker container remains") }},
		{"missing report", func(_ *npmObservation, o *execution.Outcome) { o.ReportErr = errors.New("missing report") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			var staged string
			calls := 0
			executor := Executor{AgentImage: "moving-agent-tag", Image: func(_ context.Context, r execution.ImageRequest) execution.ImageAnswer {
				if r.Tag != "moving-agent-tag" || r.Pull != "" || r.Registry != nil {
					t.Fatalf("unexpected image preparation: %+v", r)
				}
				return execution.ImageAnswer{ID: image}
			}, Run: func(_ context.Context, r execution.Request) execution.Outcome {
				calls++
				staged = r.Workspace
				if r.Name == "" || r.Image != image || r.Argv[0] != "node" || len(r.Mounts) != 0 || len(r.Env) != 0 || !r.NoEntrypoint || len(r.Files) != 1 || r.Files[0].Path != "/routine-observer.cjs" {
					t.Fatalf("validator escaped fixed isolated request: %+v", r)
				}
				for _, name := range []string{"before", "after"} {
					entries, e := os.ReadDir(filepath.Join(staged, name))
					if e != nil || len(entries) != 2 {
						t.Fatalf("unexpected staged input: %v %v", entries, e)
					}
				}
				report := routineReport(before, after)
				out := execution.Outcome{Termination: execution.Exited, ExitCode: 0}
				test.edit(&report, &out)
				raw, e := json.Marshal(report)
				if e != nil {
					t.Fatal(e)
				}
				out.Report = raw
				return out
			}}
			evidence, e := validateNPM(t.Context(), executor, data, before, after, candidate)
			if (e == nil) != (test.name == "complete") {
				t.Fatalf("evidence=%+v err=%v", evidence, e)
			}
			if calls != 1 {
				t.Fatalf("got %d executions", calls)
			}
			if _, e := os.Stat(staged); !os.IsNotExist(e) {
				t.Fatalf("local staging retained: %v", e)
			}
			if test.name == "complete" && (evidence.Image != image || !strings.Contains(evidence.ScratchSettlement, "unobserved")) {
				t.Fatalf("misreported worker evidence: %+v", evidence)
			}
		})
	}
}
