package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/verification"
)

// checkWorld is a folder with two commits and a forge that has only the
// first: published is on the forge, unpublished is the agent's newer commit
// the forge has not seen. The forge answers as its check transport does,
// with the owner's own refusal for a commit it does not have.
type checkWorld struct {
	published, unpublished string
	bodies                 *[]map[string]any
}

// withoutInheritedGit removes, for the test, the GIT_* variables the test
// run inherited: the CLI's git deliberately reads the person's whole
// environment, so a test run from a Git hook would otherwise check the
// hook's repository instead of the folder the test made. Not parallel.
func withoutInheritedGit(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
}

func newCheckWorld(t *testing.T, answerFor func(asked string) string) checkWorld {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "published")
	w := checkWorld{published: git("rev-parse", "HEAD"), bodies: &[]map[string]any{}}
	git("commit", "-q", "--allow-empty", "-m", "the agent's newer work")
	w.unpublished = git("rev-parse", "HEAD")
	withoutInheritedGit(t)
	t.Chdir(dir)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/changes/shop#7" && r.Method == "GET" {
			httpapi.WriteJSON(rw, 200, map[string]any{"projectSlug": "shop", "change": map[string]any{"id": "00000000-0007-7000-8000-000000000000", "number": 7}})
			return
		}
		if r.URL.Path != "/api/v1/changes/00000000-0007-7000-8000-000000000000/check" || r.Method != "POST" {
			httpapi.WriteError(rw, 404, "no route %s %s", r.Method, r.URL.Path)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		*w.bodies = append(*w.bodies, in)
		asked, _ := in["commit"].(string)
		if asked != "" && asked != w.published {
			httpapi.WriteFault(rw, &verification.CommitNotOnForgeError{Commit: asked})
			return
		}
		checked := w.published // no commit named: the latest the forge has
		if answerFor != nil {
			checked = answerFor(asked)
		}
		httpapi.WriteJSON(rw, 200, map[string]any{"ok": true, "commit": checked, "failOn": "high",
			"checks": []map[string]any{{"block": "b", "version": 1, "check": "builds", "verdict": "pass"}}})
	}))
	t.Cleanup(srv.Close)
	prev := api
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
	t.Cleanup(func() { api = prev })
	return w
}

// `apptown check` asks about this folder's HEAD and prints the commit the
// result is for. A HEAD the forge does not have is refused, naming it and
// how to check the published commit, and no other commit's result is shown.
func TestForgeCheckAsksAboutThisFoldersHead(t *testing.T) {
	w := newCheckWorld(t, nil)
	if _, err := gittest.Command("reset", "-q", "--hard", w.published).CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "check", "-c", "00000000-0007-7000-8000-000000000000")
	if err != nil || (*w.bodies)[0]["commit"] != w.published || len((*w.bodies)[0]) != 1 {
		t.Fatalf("a published HEAD: %v, sent %v\n%s", err, *w.bodies, out)
	}
	for _, words := range []string{"checking commit " + w.published + " (this folder's HEAD)", "commit " + w.published + "\n", "gate clear for commit " + w.published} {
		if !strings.Contains(out, words) {
			t.Fatalf("the output does not say %q:\n%s", words, out)
		}
	}

	if _, err := gittest.Command("reset", "-q", "--hard", w.unpublished).CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "check", "-c", "00000000-0007-7000-8000-000000000000")
	if err == nil || (*w.bodies)[1]["commit"] != w.unpublished {
		t.Fatalf("an unpublished HEAD was not refused: sent %v\n%s", *w.bodies, out)
	}
	for _, words := range []string{"commit " + w.unpublished + " is not on AppTown yet", "it becomes available once the step's work is collected", "apptown check --published"} {
		if !strings.Contains(err.Error(), words) {
			t.Fatalf("the refusal does not say %q: %v", words, err)
		}
	}
	if strings.Contains(out, "gate clear") || strings.Contains(out, w.published) {
		t.Fatalf("a refused check showed a result:\n%s", out)
	}
}

// --published asks for the published tip explicitly, exactly
// {"published": true} and no commit (the forge's route is tested with this
// body in internal/forge), says what it checks, and prints the commit the
// forge checked as the published tip.
func TestForgeCheckPublishedSaysWhatItChecks(t *testing.T) {
	w := newCheckWorld(t, nil)
	out, err := run(t, "check", "-c", "00000000-0007-7000-8000-000000000000", "--published")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*w.bodies) != 1 || len((*w.bodies)[0]) != 1 || (*w.bodies)[0]["published"] != true {
		t.Fatalf("--published sent %v, want exactly {\"published\": true}", *w.bodies)
	}
	for _, words := range []string{"--published: checking the published tip of change 00000000-0007-7000-8000-000000000000's branch on AppTown, not this folder's work", "commit " + w.published + " (the published tip of change 00000000-0007-7000-8000-000000000000's branch)\n", "gate clear for commit " + w.published} {
		if !strings.Contains(out, words) {
			t.Fatalf("the output does not say %q:\n%s", words, out)
		}
	}
	if strings.Contains(out, w.unpublished) {
		t.Fatalf("--published named this folder's HEAD:\n%s", out)
	}
}

// An answer for another commit than the one asked about (an older green
// result) is never shown as this work's result.
func TestForgeCheckRefusesAnAnswerForAnotherCommit(t *testing.T) {
	w := newCheckWorld(t, func(string) string { return strings.Repeat("0", 40) })
	if _, err := gittest.Command("reset", "-q", "--hard", w.published).CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "check", "-c", "00000000-0007-7000-8000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "not "+w.published+" which was asked about") || strings.Contains(out, "gate clear") {
		t.Fatalf("an answer for another commit was shown: %v\n%s", err, out)
	}
}

// Outside a Git repository nothing is named (the body is {}, not a request
// for the published tip) and the output says the forge checks the change's
// latest commit.
func TestForgeCheckOutsideARepositoryNamesNoCommit(t *testing.T) {
	w := newCheckWorld(t, nil)
	withoutInheritedGit(t)
	t.Chdir(t.TempDir())
	out, err := run(t, "check", "-c", "00000000-0007-7000-8000-000000000000")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len((*w.bodies)[0]) != 0 || !strings.Contains(out, "not in a Git repository: checking change 00000000-0007-7000-8000-000000000000's latest commit on AppTown") || !strings.Contains(out, "commit "+w.published+"\n") {
		t.Fatalf("sent %v\n%s", *w.bodies, out)
	}
}

// In an agent step on a worker ($APPTOWN_SCRATCH_REF set), `apptown check`
// first pushes this folder's HEAD to that ref on the forge's Git endpoint,
// with the step's token as a header (never in the URL), keeping the git
// configuration the environment already carries; then it asks about that
// commit, and says the answer is feedback only.
func TestForgeCheckSendsTheStepsWorkToItsScratchRefFirst(t *testing.T) {
	w := newCheckWorld(t, nil)
	head := w.unpublished // a commit the forge does not have until it is sent
	const ref = "refs/apptown/scratch/change-00000000-0007-7000-8000-000000000000/step-00000000-0003-7000-8000-000000000000-a1"
	bare := filepath.Join(t.TempDir(), "app")
	if out, err := gittest.Command("init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	var auth, paths, kept []string
	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Root: "/git",
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_PARAMETERS='http.receivepack=true'"}}
	sent := func() string {
		out, _ := gittest.Command("--git-dir", bare, "rev-parse", "--verify", "--quiet", ref).Output()
		return strings.TrimSpace(string(out))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/git/") {
			auth, paths, kept = append(auth, r.Header.Get("Authorization")), append(paths, r.URL.String()), append(kept, r.Header.Get("X-Kept"))
			backend.ServeHTTP(rw, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		*w.bodies = append(*w.bodies, in)
		if in["commit"] != sent() {
			httpapi.WriteFault(rw, &verification.CommitNotOnForgeError{Commit: fmt.Sprint(in["commit"])})
			return
		}
		httpapi.WriteJSON(rw, 200, map[string]any{"ok": true, "commit": head, "scratch": true, "feedbackOnly": true, "failOn": "high",
			"checks": []map[string]any{{"block": "b", "version": 1, "check": "builds", "verdict": "pass"}}})
	}))
	t.Cleanup(srv.Close)
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
	t.Setenv("APPTOWN_SCRATCH_REF", ref)
	t.Setenv("APPTOWN_URL", srv.URL+"/")
	t.Setenv("APPTOWN_PROJECT", "app")
	t.Setenv("APPTOWN_SESSION_TOKEN", "rt_3_1_secret")
	t.Setenv("GIT_CONFIG_COUNT", "1") // as a worker sets safe.directory
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "X-Kept: yes")

	out, err := run(t, "check", "-c", "00000000-0007-7000-8000-000000000000")
	if err != nil || sent() != head || (*w.bodies)[0]["commit"] != head {
		t.Fatalf("the step's HEAD was not sent then checked: %v, ref holds %q, asked %v\n%s", err, sent(), *w.bodies, out)
	}
	if !strings.Contains(out, "commit "+head+" (your work in progress, sent for checking: this result is feedback only, and the landing checks the published commit again)") {
		t.Fatalf("the output does not say the result is feedback only:\n%s", out)
	}
	if len(auth) == 0 {
		t.Fatal("nothing reached the Git endpoint")
	}
	for i, a := range auth {
		if a != "Bearer rt_3_1_secret" || strings.Contains(paths[i], "secret") || kept[i] != "yes" {
			t.Fatalf("request %s carried %q (the environment's own git configuration: %q)", paths[i], a, kept[i])
		}
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("the token was printed:\n%s", out)
	}
}

// The CLI's git is the person's: run from a Git hook, or with GIT_DIR set,
// it reads the repository their environment names, not the folder's. Not
// parallel: it sets the process's environment and folder.
func TestForgeCheckReadsTheRepositoryThePersonsEnvironmentNames(t *testing.T) {
	theirs := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "theirs"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = theirs
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	want, err := gittest.Command("-C", theirs, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	withoutInheritedGit(t)
	t.Chdir(t.TempDir())
	t.Setenv("GIT_DIR", filepath.Join(theirs, ".git"))
	head, dirty, err := folderHead()
	if err != nil || head != strings.TrimSpace(string(want)) || dirty {
		t.Fatalf("read %q (dirty %v, %v), want the named repository's %s", head, dirty, err, want)
	}
}

// The CLI's git reads the person's own git config, which the forge's git
// never does: with status.showUntrackedFiles=no in their global config, an
// untracked file does not make their folder uncommitted work. Not
// parallel: it sets the process's environment and folder.
func TestForgeCheckReadsThePersonsOwnGitConfig(t *testing.T) {
	theirs := t.TempDir()
	gittest.Repository(t, theirs, "main", gittest.Commit{Message: "theirs", Files: map[string]string{"a.txt": "a\n"}})
	if err := os.WriteFile(filepath.Join(theirs, "untracked.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[status]\n\tshowUntrackedFiles = no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withoutInheritedGit(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Chdir(theirs)
	if head, dirty, err := folderHead(); err != nil || head == "" || dirty {
		t.Fatalf("read %q (dirty %v, %v): the person's own config did not reach their git", head, dirty, err)
	}
}
