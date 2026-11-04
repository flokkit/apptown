package stock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/testreport"
)

// Run the shipped script in an empty project: zero is a reported count,
// rather than something inferred from Node's human-readable output.
func TestNodeStarterReportsZeroTests(t *testing.T) {
	t.Parallel()
	b, err := FS.ReadFile("seeds/video-game/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Scripts map[string]string }
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	path := filepath.Join(work, "report.xml")
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{
		Workspace: work, Argv: []string{"sh", "-c", pkg.Scripts["test"]},
		Env: []string{"APPTOWN_TEST_REPORT=" + path}, Timeout: time.Minute,
	})
	if !out.Answered() || out.ExitCode != 0 || !strings.Contains(string(out.Output), "tests 0") {
		t.Fatalf("starter test script: %+v", out)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := testreport.Read(b)
	if err != nil || r.Counts != (testreport.Counts{}) {
		t.Fatalf("zero-test report: %+v, %v", r, err)
	}
}
