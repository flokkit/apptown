package wow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/projectnotes"
	"github.com/flokkit/apptown/stock"
)

// The displayed parser and the adopted repository check accept the same file.
func TestStockFormatCheckMatchesParser(t *testing.T) {
	t.Parallel()
	blocks, _ := ReadStockGuidance(stock.FS, nil)
	var script string
	for _, b := range blocks {
		if b.Name == "repo/project-notes" {
			var checks []check.Declaration
			if err := json.Unmarshal([]byte(b.ChecksJSON), &checks); err != nil {
				t.Fatal(err)
			}
			script = checks[0].Sh
		}
	}
	if script == "" {
		t.Fatal("missing declared format check")
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"symlink", "# Project notes\n- Fact. (agent · change 1)\n", false},
		{"missing", "", true}, {"empty", "", false}, {"heading", "# Project notes\n", true},
		{"valid", "# Project notes\n- Real photos only. (Sam · change 4)\n", true},
		{"blank", "# Project notes\n\n", true},
		{"unattributed", "# Project notes\n- Real photos only.\n", false},
		{"continuation", "# Project notes\n- Fact. (agent · change 1)\n more\n", false},
		{"author space", "# Project notes\n- Fact. ( agent · change 1)\n", false},
		{"empty sentence", "# Project notes\n-  . (agent · change 1)\n", false},
		{"max number", "# Project notes\n- Fact. (agent · change 9223372036854775807)\n", true},
		{"overflow", "# Project notes\n- Fact. (agent · change 9223372036854775808)\n", false},
		{"zero", "# Project notes\n- Fact. (agent · change 0)\n", false},
		{"too many", "# Project notes\n" + strings.Repeat("- Fact. (agent · change 1)\n", 41), false},
		{"too large", "# Project notes\n- " + strings.Repeat("x", 6144) + ". (agent · change 1)\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var body []byte
			if tc.name == "symlink" {
				if err := os.WriteFile(filepath.Join(dir, "target.md"), []byte(tc.body), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target.md", filepath.Join(dir, projectnotes.Path)); err != nil {
					t.Fatal(err)
				}
			} else if tc.name != "missing" {
				body = []byte(tc.body)
				if err := os.WriteFile(filepath.Join(dir, projectnotes.Path), body, 0644); err != nil {
					t.Fatal(err)
				}
			}
			gittest.Run(t, dir, "init", "-b", "main")
			gittest.Run(t, dir, "add", "-A")
			gittest.Run(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "notes")
			_, _, err := projectnotes.Read(t.Context(), dir, "HEAD")
			if (err == nil) != tc.valid {
				t.Fatalf("parser: %v", err)
			}
			result := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: dir, Timeout: time.Minute, Argv: []string{"sh", "-c", script}})
			if !result.Answered() {
				t.Fatalf("format check could not run: %+v", result)
			}
			out := result.Output
			if result.Exited0() != tc.valid {
				t.Fatalf("check exit %d: %s", result.ExitCode, out)
			}
			if !tc.valid && !strings.Contains(string(out), projectnotes.Path+":") {
				t.Fatalf("failure has no file/line: %s", out)
			}
		})
	}
}
