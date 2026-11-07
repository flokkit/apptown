//go:build integration

package stock

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/testreport"
)

// A first app change enables the seeded Makefile and declares it on the web
// part. Execute that declaration in a fresh project, with the report path the
// tests step supplies, and read it through the same report parser. Requires uv
// and package downloads; run with the integration tier.
func TestIntegrationDjangoSeedRecordsTestCounts(t *testing.T) {
	work := t.TempDir()
	run := func(argv []string, env []string, want int) execution.Outcome {
		t.Helper()
		out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{
			Workspace: work, Argv: argv, Env: env, Timeout: 2 * time.Minute,
		})
		if !out.Answered() || out.ExitCode != want {
			t.Fatalf("%v: exit %d (%s)\n%s", argv, out.ExitCode, out.Termination, out.Output)
		}
		return out
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run([]string{"uv", "init", "--bare", "--name", "app"}, nil, 0)
	run([]string{"uv", "add", "django==5.2.8"}, nil, 0)
	run([]string{"uv", "run", "django-admin", "startproject", "app", "."}, nil, 0)
	if err := fs.WalkDir(FS, "seeds/django", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := filepath.Join(work, strings.TrimPrefix(p, "seeds/django"))
		if d.IsDir() {
			return os.MkdirAll(name, 0o755)
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(name, b, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	run([]string{"uv", "add", "--dev", "-r", "requirements-test.txt"}, nil, 0)
	makefile, err := os.ReadFile(filepath.Join(work, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// Enable only the commented target, leaving explanatory comments intact.
	lines := strings.Split(string(makefile), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "#.PHONY:") || strings.HasPrefix(line, "#test:") || strings.HasPrefix(line, "#\t") {
			lines[i] = strings.TrimPrefix(line, "#")
		}
	}
	write("Makefile", strings.Join(lines, "\n"))
	contract, err := os.ReadFile(filepath.Join(work, ".apptown/app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	write(".apptown/app.yaml", strings.Replace(string(contract), "    # test:", "    test:", 1))
	write("app/urls.py", "urlpatterns = []\n")
	write("app/settings.py", "from app.deploy_settings import *\nROOT_URLCONF = 'app.urls'\nALLOWED_HOSTS = ['localhost']\nMIDDLEWARE = ['app.health.healthz', 'django.middleware.security.SecurityMiddleware']\n")
	c, err := docker.ReadWorkspaceContract(work)
	if err != nil || len(c.Parts) != 1 || len(c.Parts[0].Test) != 1 {
		t.Fatalf("enabled seed contract: %+v, %v", c, err)
	}
	// The tests step starts without a virtualenv; the declared command must install.
	if err := os.RemoveAll(filepath.Join(work, ".venv")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, ".forge-test-report")
	env := []string{"APPTOWN_TEST_REPORT=" + path}
	for _, tc := range []struct {
		name, extra string
		exit        int
		counts      testreport.Counts
	}{
		{"seed", "", 0, testreport.Counts{Passed: 2}},
		{"failed and skipped", "from django.test import SimpleTestCase\nfrom unittest import skip\nclass Outcomes(SimpleTestCase):\n    def test_fails(self): self.fail('failure recorded')\n    @skip('skip recorded')\n    def test_skips(self): pass\n", 2, testreport.Counts{Passed: 2, Failed: 1, Skipped: 1}},
	} {
		t.Log(tc.name)
		write("app/test_outcomes.py", tc.extra)
		out := run([]string{"sh", "-c", c.Parts[0].Test[0]}, env, tc.exit)
		if !strings.Contains(string(out.Output), "Ran ") {
			t.Fatalf("no readable test log: %s", out.Output)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := testreport.Read(b)
		if err != nil || r.Counts != tc.counts {
			t.Fatalf("report: %+v, %v; want %+v", r, err, tc.counts)
		}
		t.Logf("recorded passed=%d failed=%d skipped=%d (exit %d)", r.Counts.Passed, r.Counts.Failed, r.Counts.Skipped, out.ExitCode)
	}
}
