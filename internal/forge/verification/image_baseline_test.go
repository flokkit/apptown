package verification

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
)

// When the starting revision gives no image, a project that has never
// released says it is the first release, and keeps why the starting
// revision gave no image as the detail: a seed that cannot build yet is
// not a fault (soak finding 7). A project that has released, or whose
// deployment cannot be read, keeps the preparation in the note.
//
// Kills: the first-release branch removed (the never-released cases read
// "preparing starting revision's image failed"), and the ReleaseSHA test
// dropped (the released case reads "first release").
func TestImageBaselineExplainsMissingImageAndPreparationFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contract, want                               string
		badRevision, buildFailure, lookupFailure, released bool
	}{
		{name: "static", contract: "app:\n  web:\n    static: .\n    public: true\n", want: "no image of the starting revision to compare with"},
		{name: "no contract", want: "no image of the starting revision to compare with: no app contract"},
		{name: "invalid contract", contract: "[", want: "preparing starting revision's image failed: reading app contract"},
		{name: "release lookup", lookupFailure: true, want: "reading the serving release failed"},
		{name: "staging", badRevision: true, want: "staging starting revision"},
		{name: "build", contract: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", buildFailure: true, want: "building the app image: builder unavailable: build refused"},
		{name: "build, released", contract: "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", buildFailure: true, released: true, want: "building the app image: builder unavailable: build refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newScannerOwnerFixture(t)
			f.Scanners.deployments = deployment.NewDeployments(f.DB)
			repo, sha := baselineTree(t, tc.contract)
			if tc.released {
				// main has served a release, from a container this host
				// does not run.
				servingMain(t, f, f.Scanners.deployments, sha)
			}
			if tc.badRevision {
				sha = "missing-revision"
			}
			if tc.buildFailure {
				f.Daemon.SetReply(func(args []string) ([]byte, error, bool) {
					if len(args) > 0 && args[0] == "image" {
						return nil, errors.New("cache miss"), true
					}
					if len(args) > 0 && args[0] == "build" {
						return []byte("build refused"), errors.New("builder unavailable"), true
					}
					return nil, nil, false
				})
			}
			if tc.lookupFailure {
				t.Cleanup(f.DB.FailNextStatement("FROM deployments"))
			}
			result := f.Scanners.baseImageFingerprints(f.Context, f.ProjectID, repo, sha, check.Scanner{}, "scanner", t.TempDir(), nil, "scan")
			if result.OK {
				t.Fatalf("baseline: %+v", result)
			}
			if tc.released || tc.lookupFailure {
				if !strings.Contains(result.Note, tc.want) || !strings.Contains(result.Note, "every finding is treated as introduced") || strings.Contains(result.Note, "first release") {
					t.Fatalf("baseline: %+v", result)
				}
				return
			}
			if result.Note != firstReleaseNote || !strings.Contains(result.Detail, tc.want) {
				t.Fatalf("first release baseline: %+v", result)
			}
		})
	}
}

func TestFirstLandingUsesStartingImageWithoutAServingRelease(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.Scanners.deployments = deployment.NewDeployments(f.DB)
	repo, sha := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	scan := t.TempDir()
	// Fake only the external scanner process; image preparation and comparison
	// use the real owner. The result has an old finding, not merely an empty pass.
	binary := scannerProcess(t, 0)
	// Images come from the fake daemon; the scanner process is the script.
	f.Scanners.exec.Run = LocalExecutor(execution.Runner{DockerBinary: binary}, f.Scanners.docker, "", nil).Run
	base := f.Scanners.baseImageFingerprints(f.Context, f.ProjectID, repo, sha, check.Scanner{Image: "scanner", Format: "sarif", Report: ".forge/semgrep.sarif", Kind: "sast"}, "scanner", scan, []string{"scan", "${IMAGE_TAR}"}, "scan")
	if !base.OK || len(base.Findings) != 1 || !strings.Contains(base.Note, "compared with starting revision's image at ") {
		t.Fatalf("baseline: %+v", base)
	}
	introduced, carried := splitByProvenance(conformance.WrapFindings(base.Findings), base)
	if len(introduced) != 0 || len(carried) != 1 {
		t.Fatalf("existing finding blamed on first landing: %v %v", introduced, carried)
	}
}

func baselineTree(t *testing.T, contract string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if contract != "" {
		if err := os.Mkdir(filepath.Join(repo, ".forge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".forge", "app.yaml"), []byte(contract), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "--allow-empty", "-qm", "baseline")
	return repo, git("rev-parse", "HEAD")
}
