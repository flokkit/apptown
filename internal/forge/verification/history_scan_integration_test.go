//go:build integration

package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/stock"
)

// Real stock gitleaks: a token introduced only in a merge resolution and
// removed in the next commit is still new history the landing adds.
func TestIntegrationTheStockHistoryScanFindsASecretInAMergeResolution(t *testing.T) {
	var scanner check.Scanner
	for _, s := range loadScannerManifests(stock.FS) {
		if s.Kind == "secret" {
			scanner = s
			break
		}
	}
	if scanner.Image == "" || len(scanner.HistoryCommand) == 0 {
		t.Fatal("no history secret scanner")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Review")
	git("config", "user.email", "review@example.invalid")
	write("base", "base\n")
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-qb", "side")
	write("side", "safe\n")
	git("add", ".")
	git("commit", "-qm", "side")
	git("checkout", "main")
	write("main", "safe\n")
	git("add", ".")
	git("commit", "-qm", "main")
	git("merge", "--no-ff", "--no-commit", "side")
	// Synthetic, nonfunctional token; split so the fixture itself is not a token.
	write("secret.txt", `github_token = "`+"ghp_"+"m8Xq7Np3Rz9Kd2Wv6Bj4Ts1Lf5Yc0Ha8Eu3P"+"\"\n")
	git("add", ".")
	git("commit", "-qm", "merge resolution")
	git("rm", "secret.txt")
	git("commit", "-qm", "remove token")
	commits := base + ".." + git("rev-parse", "HEAD")
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".forge"), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rm", "--network", "none", "-v", repo + ":/history:ro", "-v", work + ":/work", scanner.Image}
	for _, arg := range scanner.HistoryCommand {
		args = append(args, strings.ReplaceAll(arg, "${HISTORY_RANGE}", commits))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	control := append([]string{}, args...)
	for i, arg := range control {
		if arg == commits {
			control[i] = commits + " --diff-merges=separate"
		}
	}
	controlOut, controlErr := (docker.CLI{}).Run(ctx, "", docker.Command{Args: control})
	if ee, ok := controlErr.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("control did not detect synthetic token: %v: %s", controlErr, controlOut)
	}
	out, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: args})
	if err == nil {
		t.Fatalf("history secret scanner passed a merge-introduced token: %s", out)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("scanner infrastructure failure: %v: %s", err, out)
	}
}
