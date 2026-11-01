package scm

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// source is a repository with main (two commits, tagged), and another
// branch nobody asked for.
func source(t *testing.T) (dir, main, tagged string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "source")
	gittest.RunAsTester(t, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	if err := os.WriteFile(filepath.Join(dir, "mise.toml"), []byte("[tools]\ngo = \"1.23\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, dir, "add", ".")
	gittest.RunAsTester(t, dir, "commit", "-qm", "first")
	tagged = gittest.RunAsTester(t, dir, "rev-parse", "HEAD")
	gittest.RunAsTester(t, dir, "tag", "-a", "v1", "-m", "v1")
	gittest.RunAsTester(t, dir, "commit", "-q", "--allow-empty", "-m", "second")
	main = gittest.RunAsTester(t, dir, "rev-parse", "HEAD")
	gittest.RunAsTester(t, dir, "checkout", "-q", "-b", "work-in-flight")
	gittest.RunAsTester(t, dir, "commit", "-q", "--allow-empty", "-m", "unfinished")
	gittest.RunAsTester(t, dir, "checkout", "-q", "main")
	return dir, main, tagged
}

func bundleOf(t *testing.T, repo string, refs ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "p.bundle")
	gittest.RunAsTester(t, repo, append([]string{"bundle", "create", "-q", out}, refs...)...)
	return out
}

// A bundle starts the hub with its default branch and tags, at the same
// commits, with the tree checked out and no other branch.
func TestABundleStartsTheHubWithItsDefaultBranchAndTags(t *testing.T) {
	t.Parallel()
	src, main, tagged := source(t)
	ctx := context.Background()
	for name, refs := range map[string][]string{
		"exactly `git bundle create p.bundle main`": {"main", "--tags"},
		"a bundle whose HEAD names main":            {"HEAD", "main", "work-in-flight", "--tags"},
	} {
		t.Run(name, func(t *testing.T) {
			hub := filepath.Join(t.TempDir(), "project-x")
			branch, err := SeedFromBundle(ctx, hub, bundleOf(t, src, refs...), "")
			if err != nil || branch != "main" {
				t.Fatalf("seed = %q %v", branch, err)
			}
			if got := gittest.RunAsTester(t, hub, "rev-parse", "refs/heads/main"); got != main {
				t.Fatalf("main = %s, want %s", got, main)
			}
			if got := gittest.RunAsTester(t, hub, "rev-parse", "v1^{commit}"); got != tagged {
				t.Fatalf("v1 = %s, want %s", got, tagged)
			}
			if got := gittest.RunAsTester(t, hub, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
				t.Fatalf("branches = %q", got)
			}
			if _, err := os.Stat(filepath.Join(hub, "mise.toml")); err != nil {
				t.Fatalf("the tree is not checked out: %v", err)
			}
			if out := gittest.RunAsTester(t, hub, "status", "--porcelain"); out != "" {
				t.Fatalf("the hub is dirty: %q", out)
			}
		})
	}
}

// A bundle that does not say which branch is the default is refused, with
// its branches named, unless the caller names one.
func TestABundleWithNoSingleDefaultIsRefused(t *testing.T) {
	t.Parallel()
	src, _, _ := source(t)
	ctx := context.Background()
	b := bundleOf(t, src, "main", "work-in-flight")
	hub := filepath.Join(t.TempDir(), "project-x")
	_, err := SeedFromBundle(ctx, hub, b, "")
	var why *sourceRefused
	if !errors.As(err, &why) || !strings.Contains(why.Why, "main, work-in-flight") || fault.KindOf(err) != fault.Invalid {
		t.Fatalf("an ambiguous bundle = %v", err)
	}
	if _, err := os.Stat(hub); !os.IsNotExist(err) {
		t.Fatalf("a refused seed left a hub: %v", err)
	}
	if branch, err := SeedFromBundle(ctx, hub, b, "work-in-flight"); err != nil || branch != "work-in-flight" {
		t.Fatalf("a named branch = %q %v", branch, err)
	}
	if _, err := SeedFromBundle(ctx, filepath.Join(t.TempDir(), "y"), b, "nope"); !errors.As(err, &why) {
		t.Fatalf("a missing named branch = %v", err)
	}
}

// A default branch named from outside (the caller's, or a remote's HEAD)
// that git would not take as a branch name is refused before anything is
// fetched, and leaves no hub.
func TestASeedRefusesANameThatIsNotABranchName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, branch := range []string{"-main", "HEAD", "main..x", "main.lock", "@{-1}"} {
		hub := filepath.Join(t.TempDir(), "project-x")
		_, err := SeedFromRemote(ctx, hub, gitexec.Remote{URL: "https://example.invalid/x.git"}, branch)
		var why *sourceRefused
		if !errors.As(err, &why) || !strings.Contains(why.Why, "is not a branch name") {
			t.Fatalf("branch %q: %v", branch, err)
		}
		if _, err := os.Stat(hub); !os.IsNotExist(err) {
			t.Fatalf("branch %q left a hub: %v", branch, err)
		}
	}
}

// What is not a whole repository is refused and leaves no hub: damage, an
// incremental bundle that needs history it does not carry, a non-bundle.
func TestABrokenBundleIsRefusedAndLeavesNoHub(t *testing.T) {
	t.Parallel()
	src, _, tagged := source(t)
	ctx := context.Background()
	good, err := os.ReadFile(bundleOf(t, src, "main"))
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte{}, good...)
	damaged[len(damaged)-30] ^= 0xff
	incremental := bundleOf(t, src, tagged+"..main")
	for name, path := range map[string]string{
		"damaged":      write(t, damaged),
		"incremental":  incremental,
		"not a bundle": write(t, []byte("hello")),
	} {
		hub := filepath.Join(t.TempDir(), "project-x")
		if _, err := SeedFromBundle(ctx, hub, path, "main"); err == nil {
			t.Fatalf("%s: seeded", name)
		}
		if _, err := os.Stat(hub); !os.IsNotExist(err) {
			t.Fatalf("%s: a refused seed left a hub: %v", name, err)
		}
	}
}

func write(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.bundle")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A hub with history is never replaced.
func TestASeedRefusesAHubWithHistory(t *testing.T) {
	t.Parallel()
	src, main, _ := source(t)
	ctx := context.Background()
	hub := filepath.Join(t.TempDir(), "project-x")
	if _, err := SeedFromBundle(ctx, hub, bundleOf(t, src, "main"), ""); err != nil {
		t.Fatal(err)
	}
	other, _, _ := source(t)
	gittest.RunAsTester(t, other, "commit", "-q", "--allow-empty", "-m", "different")
	if _, err := SeedFromBundle(ctx, hub, bundleOf(t, other, "main"), ""); !errors.Is(err, ErrRepositoryEstablished) {
		t.Fatalf("a second seed = %v", err)
	}
	if got := gittest.RunAsTester(t, hub, "rev-parse", "main"); got != main {
		t.Fatalf("the refused seed moved main to %s", got)
	}
}

// The Download code bundle carries the default branch and tags, says which
// branch is the default, and starts another hub at the same commits.
func TestTheDownloadBundleSeedsAnotherHubUnchanged(t *testing.T) {
	t.Parallel()
	src, main, tagged := source(t)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := WriteBundle(ctx, src, "main", &buf); err != nil {
		t.Fatal(err)
	}
	b := write(t, buf.Bytes())
	heads := gittest.RunAsTester(t, src, "bundle", "list-heads", b)
	if strings.Contains(heads, "work-in-flight") || !strings.Contains(heads, " HEAD") {
		t.Fatalf("bundle heads = %q", heads)
	}
	hub := filepath.Join(t.TempDir(), "project-y")
	if branch, err := SeedFromBundle(ctx, hub, b, ""); err != nil || branch != "main" {
		t.Fatalf("seed = %q %v", branch, err)
	}
	if gittest.RunAsTester(t, hub, "rev-parse", "main") != main || gittest.RunAsTester(t, hub, "rev-parse", "v1^{commit}") != tagged {
		t.Fatal("the round trip changed the history")
	}
}

// The server fetches only https on port 443, with no credential in the URL,
// from public addresses, pinned to the address it checked.
func TestTheURLPolicyAdmitsOnlyPublicHTTPS(t *testing.T) {
	t.Parallel()
	answers := map[string][]string{
		"public.example": {"93.184.215.14"},
		"mixed.example":  {"93.184.215.14", "10.0.0.5"},
		"v6.example":     {"2606:2800:21f:cb07:6820:80da:af6b:8b2c"},
		"localhost":      {"127.0.0.1"},
		"lan.example":    {"192.168.1.10"},
		"meta.example":   {"169.254.169.254"},
		"cgnat.example":  {"100.64.1.1"},
		"ula.example":    {"fd00::1"},
		"mapped.example": {"::ffff:10.1.2.3"},
		"zero.example":   {"0.0.0.0"},
		"mcast.example":  {"224.0.0.1"},
	}
	policy := URLPolicy{Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range answers[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}}
	ctx := context.Background()
	r, err := policy.Check(ctx, "https://public.example/team/app.git")
	if err != nil || r.URL != "https://public.example/team/app.git" || r.Pin != "public.example:443:93.184.215.14" {
		t.Fatalf("public = %+v %v", r, err)
	}
	if r, err := policy.Check(ctx, "https://public.example:443/a.git"); err != nil || r.Pin != "public.example:443:93.184.215.14" {
		t.Fatalf("explicit 443 = %+v %v", r, err)
	}
	if r, err := policy.Check(ctx, "https://v6.example/a.git"); err != nil || r.Pin != "v6.example:443:[2606:2800:21f:cb07:6820:80da:af6b:8b2c]" {
		t.Fatalf("v6 = %+v %v", r, err)
	}
	for _, raw := range []string{
		"http://public.example/a.git", "ssh://git@public.example/a.git", "git://public.example/a.git",
		"file:///etc/passwd", "/srv/repo", "git@public.example:a.git", "https://user:pw@public.example/a.git",
		"https://token@public.example/a.git", "https://public.example:8443/a.git",
		"https://mixed.example/a.git", "https://localhost/a.git", "https://lan.example/a.git", "https://meta.example/a.git",
		"https://cgnat.example/a.git", "https://ula.example/a.git", "https://mapped.example/a.git", "https://zero.example/a.git",
		"https://mcast.example/a.git", "https://127.0.0.1/a.git", "https://[::1]/a.git", "https://nowhere.example/a.git",
	} {
		_, err := policy.Check(ctx, raw)
		var why *sourceRefused
		if !errors.As(err, &why) {
			t.Fatalf("%s was admitted: %v", raw, err)
		}
	}
	policy.AllowPrivate = true
	if r, err := policy.Check(ctx, "https://lan.example/a.git"); err != nil || r.Pin != "lan.example:443:192.168.1.10" {
		t.Fatalf("an allowed private address = %+v %v", r, err)
	}
	if _, err := policy.Check(ctx, "https://lan.example:8443/a.git"); err == nil {
		t.Fatal("allowing private addresses allowed another port")
	}
}
