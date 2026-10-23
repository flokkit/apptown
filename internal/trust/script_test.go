package trust_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The script a container runs is the installer with the deployments bundle
// in it: here (on this machine) with the local authority too, elsewhere
// without it, and none when there is nothing to trust. It holds no ${,
// which App Platform would rewrite in an environment value.
func TestTheScriptIsTheInstallerWithTheDeploymentsBundle(t *testing.T) {
	t.Parallel()
	company, local := trusttest.New(t, "Company CA"), trusttest.New(t, "Local CA")
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: company.Bundle(t), trust.Inference: trusttest.New(t, "Inference CA").Bundle(t)}, local.Bundle(t))
	if err != nil {
		t.Fatal(err)
	}
	here, elsewhere := set.Script(true), set.Script(false)
	pemOf := func(a *trusttest.Authority) string { return strings.TrimSpace(string(a.PEM())) }
	if !strings.Contains(here, pemOf(company)) || !strings.Contains(here, pemOf(local)) {
		t.Fatalf("the script for this machine lacks an authority:\n%s", here)
	}
	if !strings.Contains(elsewhere, pemOf(company)) || strings.Contains(elsewhere, pemOf(local)) {
		t.Fatalf("the script for elsewhere must hold the company's authority and not the local one:\n%s", elsewhere)
	}
	for _, sc := range []string{here, elsewhere} {
		if strings.Contains(sc, "${") || strings.Contains(sc, "Inference CA") || !strings.HasSuffix(sc, "apptown_trust\n") {
			t.Fatalf("the script:\n%s", sc)
		}
		for _, line := range strings.Split(sc, "\n") {
			if strings.HasPrefix(line, "#") {
				t.Fatalf("a comment in the script: %q", line)
			}
		}
	}
	only, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{}, local.Bundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if only.Script(true) == "" || only.Script(false) != "" {
		t.Fatal("the local authority alone is for this machine's containers only")
	}
	if none, _ := trust.New(t.TempDir(), nil, trust.Bundle{}); none.Script(true) != "" || none.Script(false) != "" {
		t.Fatal("a script with nothing to trust")
	}
}

// Installed, the script is this process's APPTOWN_TRUST, and a container or
// build is given it by that name (a build also its SHA-256, which a Dockerfile
// can make a cache key of); with nothing to trust no argument is
// passed and an inherited value is removed.
// Not parallel: it installs the process's trust.
func TestTheScriptIsPassedByName(t *testing.T) {
	t.Setenv(trust.EnvVar, "inherited")
	trust.Install(nil)
	if _, set := os.LookupEnv(trust.EnvVar); set || trust.ContainerArgs(true) != nil || trust.BuildArgs(true) != nil {
		t.Fatal("with nothing to trust, a script is still passed")
	}
	set := trusttest.Install(t, trust.Bundle{}, trusttest.New(t, "Local CA").Bundle(t))
	if os.Getenv(trust.EnvVar) != set.Script(true) {
		t.Fatal("the process's variable is not this machine's script")
	}
	if !slices.Equal(trust.ContainerArgs(true), []string{"-e", trust.EnvVar}) || trust.ContainerArgs(false) != nil {
		t.Fatalf("container arguments %v, elsewhere %v", trust.ContainerArgs(true), trust.ContainerArgs(false))
	}
	sum := fmt.Sprintf("APPTOWN_TRUST_SUM=%x", sha256.Sum256([]byte(set.Script(true))))
	if !slices.Equal(trust.BuildArgs(true), []string{"--secret", "id=apptown-trust,env=APPTOWN_TRUST", "--build-arg", sum}) || trust.BuildArgs(false) != nil {
		t.Fatalf("build arguments %v, elsewhere %v", trust.BuildArgs(true), trust.BuildArgs(false))
	}
}

// Run in a shell that stops at the first error or unset variable, the
// script never stops it, sets the variables to the roots it found plus the
// extras (APPTOWN_TRUST_CERTS to the extras alone), and run again changes
// nothing.
func TestTheScriptNeverStopsTheShellAndRunsAgainUnchanged(t *testing.T) {
	t.Parallel()
	company := trusttest.New(t, "Company CA")
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: company.Bundle(t)}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	show := `echo "SSL=$SSL_CERT_FILE REQ=$REQUESTS_CA_BUNDLE GIT=$GIT_SSL_CAINFO NODE=$NODE_EXTRA_CA_CERTS CERTS=$APPTOWN_TRUST_CERTS"`
	out := execution.Runner{}.Run(context.Background(), execution.Local, execution.Request{
		Workspace: tmp,
		Timeout:   time.Minute,
		Argv:      []string{"sh", "-c", "set -eu; " + trust.Run + "; " + show + "; " + trust.Run + "; " + show + "; echo went-on"},
		Env:       []string{trust.EnvVar + "=" + set.Script(true), "TMPDIR=" + tmp, "HOME=" + tmp, "PATH=/usr/bin:/bin"},
	})
	if out.RunErr != nil || out.ExitCode != 0 {
		t.Fatalf("%v (exit %d): %s", out.RunErr, out.ExitCode, out.Output)
	}
	lines := strings.Split(strings.TrimSpace(string(out.Output)), "\n")
	var shown []string
	for _, l := range lines {
		if strings.HasPrefix(l, "SSL=") {
			shown = append(shown, l)
		}
	}
	if lines[len(lines)-1] != "went-on" || len(shown) != 2 || shown[0] != shown[1] {
		t.Fatalf("output:\n%s", out.Output)
	}
	matches, _ := filepath.Glob(filepath.Join(tmp, "apptown-trust-*"))
	if len(matches) != 1 {
		t.Fatalf("folders %v", matches)
	}
	dir := matches[0]
	roots := filepath.Join(dir, "roots.pem")
	extras := filepath.Join(dir, "extras.pem")
	if want := "SSL=" + roots + " REQ=" + roots + " GIT=" + roots + " NODE=" + extras + " CERTS=" + extras; shown[0] != want {
		t.Fatalf("variables %q, want %q", shown[0], want)
	}
	body, err := os.ReadFile(roots)
	pemText := strings.TrimSpace(string(company.PEM()))
	if err != nil || strings.Count(string(body), pemText) != 1 {
		t.Fatalf("the roots file must hold the extra authority once: %v", err)
	}
	alone, err := os.ReadFile(extras)
	if err != nil || strings.TrimSpace(string(alone)) != pemText {
		t.Fatalf("APPTOWN_TRUST_CERTS must hold the extra authority alone: %v\n%s", err, alone)
	}
}
