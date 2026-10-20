package forge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec"

	"flokkit.io/forge/internal/forge/project"
)

// The scoped git endpoint: a token holder can clone/fetch main, cannot see or
// fetch another change's branch, and cannot push.
func TestGitHTTPScoped(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	slug := "gh"
	pid := plainTestProject(t, a.DB, slug, "G")
	p := &project.Project{ID: pid, Slug: slug}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	hub := a.projectRepoPath(slug)
	_ = os.WriteFile(filepath.Join(hub, "readme.txt"), []byte("on main\n"), 0o644)
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "main content"); err != nil {
		t.Fatalf("main commit: %s", out)
	}

	// Another change's branch with a secret file — must stay invisible.
	wt := filepath.Join(a.cfg.dataDir, "workspaces", "change-00000000-0001-7000-8000-000000000000")
	if err := ensureWorkspaceCloneForTest(a, hub, wt, "forge/change-00000000-0001-7000-8000-000000000000"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt, "secret.txt"), []byte("another change's private work\n"), 0o644)
	_, _ = gitexec.Run(t.Context(), wt, "add", "-A")
	_, _ = gitexec.Run(t.Context(), wt, "commit", "-m", "private")
	mirrorBranchForTest(hub, wt, "forge/change-00000000-0001-7000-8000-000000000000")

	tok, err := a.Access.RuntimePullToken(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	url := strings.Replace(srv.URL, "http://", "http://x-token:"+tok+"@", 1) + "/git/" + slug

	// Clone through the endpoint — succeeds, gets main.
	clone := filepath.Join(t.TempDir(), "c")
	if out, err := gitexec.Run(t.Context(), "", "clone", "--quiet", url, clone); err != nil {
		t.Fatalf("scoped clone must succeed: %s", out)
	}
	if _, err := os.Stat(filepath.Join(clone, "readme.txt")); err != nil {
		t.Fatal("clone did not receive main's content")
	}
	if _, err := os.Stat(filepath.Join(clone, "secret.txt")); err == nil {
		t.Fatal("clone received another change's private file")
	}

	// ls-remote advertises ONLY main — the change branch is hidden.
	ls, _ := gitexec.Run(t.Context(), clone, "ls-remote", "origin")
	if !strings.Contains(ls, "refs/heads/main") {
		t.Fatalf("main must be advertised: %s", ls)
	}
	if strings.Contains(ls, "forge/change-00000000-0001-7000-8000-000000000000") {
		t.Fatalf("another change's branch leaked in the advertisement: %s", ls)
	}

	// Push is refused.
	_, _ = gitexec.Run(t.Context(), clone, "commit", "--allow-empty", "-m", "try to push")
	if out, err := gitexec.Run(t.Context(), clone, "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("push must be refused, but it succeeded: %s", out)
	}

	// A request whose service query names another service than its path is
	// refused before git runs, whatever the credential.
	for _, path := range []string{"/git-upload-pack?service=git-receive-pack", "/git-receive-pack?service=git-upload-pack"} {
		req, _ := http.NewRequest("POST", srv.URL+"/git/"+slug+path, strings.NewReader("0000"))
		req.SetBasicAuth("x-token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "does not match its path") {
			t.Fatalf("POST %s: %d %s", path, resp.StatusCode, body)
		}
	}

	// Git's old plain-file reads serve nothing: neither the ref listing
	// (which would name the hidden branch) nor another change's object by
	// its name (http.getanyfile=false for every credential).
	private, err := gitexec.Run(t.Context(), hub, "rev-parse", "refs/heads/forge/change-00000000-0001-7000-8000-000000000000")
	if err != nil {
		t.Fatalf("private tip: %s", private)
	}
	private = strings.TrimSpace(private)
	for _, path := range []string{"/info/refs", "/objects/" + private[:2] + "/" + private[2:], "/objects/info/packs", "/HEAD"} {
		req, _ := http.NewRequest("GET", srv.URL+"/git/"+slug+path, nil)
		req.SetBasicAuth("x-token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "forge/change-00000000-0001-7000-8000-000000000000") {
			t.Fatalf("plain-file GET %s served %d: %.120s", path, resp.StatusCode, body)
		}
	}

	// A bogus token gets nothing.
	badURL := strings.Replace(srv.URL, "http://", "http://x-token:nope@", 1) + "/git/" + slug
	if out, err := gitexec.Run(t.Context(), "", "clone", "--quiet", badURL, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatalf("an invalid token must be rejected: %s", out)
	}

	// A chat reaches the endpoint through the read-only listener, with a
	// plain URL and its token as a header that git sends to that URL only
	// (conversation's APPTOWN_MAIN_REMOTE). Clone and a later fetch of a
	// moved main both work there.
	ro := httptest.NewServer(GETOnly(a.Handler()))
	defer ro.Close()
	plain := ro.URL + "/git/" + slug
	header := "http." + plain + ".extraHeader=Authorization: Bearer " + tok
	chat := filepath.Join(t.TempDir(), "h")
	if out, err := gitexec.Run(t.Context(), "", "-c", header, "clone", "--quiet", plain, chat); err != nil {
		t.Fatalf("the token as a header must open the endpoint: %s", out)
	}
	_ = os.WriteFile(filepath.Join(hub, "later.txt"), []byte("landed later\n"), 0o644)
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "main moved"); err != nil {
		t.Fatalf("main commit: %s", out)
	}
	if out, err := gitexec.Run(t.Context(), chat, "-c", header, "fetch", "--quiet", plain, "main"); err != nil {
		t.Fatalf("catching up with main through the read-only listener: %s", out)
	}
	if out, err := gitexec.Run(t.Context(), chat, "cat-file", "-e", "FETCH_HEAD:later.txt"); err != nil {
		t.Fatalf("the fetch did not bring main's new commit: %s", out)
	}
	if out, err := gitexec.Run(t.Context(), chat, "-c", header, "push", plain, "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("a push through the read-only listener must be refused: %s", out)
	}
}

// The service a git request runs is read as git's backend reads it, from
// its method and path; the service query counts only on info/refs
// discovery. A query naming another service than the path, or a service on
// any other path, is a conflict, refused whatever the credential.
func TestAGitRequestsServiceIsItsMethodAndPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		method, path, query, service string
		ok                           bool
	}{
		{"GET", "info/refs", "service=git-receive-pack", "git-receive-pack", true},
		{"HEAD", "info/refs", "service=git-receive-pack", "git-receive-pack", true},
		{"GET", "info/refs", "service=git-upload-pack", "git-upload-pack", true},
		{"GET", "info/refs", "", "", true},
		{"GET", "info/refs", "service=git-archive", "", false},
		{"POST", "info/refs", "service=git-receive-pack", "", false},
		{"POST", "git-receive-pack", "", "git-receive-pack", true},
		{"POST", "git-upload-pack", "", "git-upload-pack", true},
		{"POST", "git-upload-pack", "service=git-receive-pack", "git-upload-pack", false},
		{"POST", "git-receive-pack", "service=git-upload-pack", "git-receive-pack", false},
		{"POST", "git-receive-pack", "service=git-receive-pack", "git-receive-pack", true},
		{"GET", "git-receive-pack", "", "", true},
		{"GET", "git-upload-pack", "service=git-receive-pack", "", false},
		{"GET", "objects/info/packs", "service=git-receive-pack", "", false},
		{"GET", "objects/info/packs", "", "", true},
		{"GET", "HEAD", "service=git-receive-pack", "", false},
	} {
		q, _ := url.ParseQuery(c.query)
		service, ok := gitService(c.method, c.path, q)
		if ok != c.ok || (ok && service != c.service) {
			t.Errorf("%s %s?%s: %q %v, want %q %v", c.method, c.path, c.query, service, ok, c.service, c.ok)
		}
	}
}
