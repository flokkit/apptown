package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestJanitorScopeAnswersOnlyForTheAskedCommit(t *testing.T) {
	w := newCheckWorld(t, nil)
	t.Setenv("APPTOWN_SCRATCH_REF", "")
	t.Setenv("APPTOWN_RUN_ID", "00000000-0007-7000-8000-000000000000")
	answer, status := w.unpublished, "pass"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/scope") {
			t.Errorf("wrong route: %s %s", r.Method, r.URL.Path)
		}
		var in httpapi.RunScopeRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Commit != w.unpublished {
			t.Errorf("wrong commit: %+v %v", in, err)
		}
		httpapi.WriteJSON(rw, 200, map[string]string{"commit": answer, "status": status, "reason": "judge unavailable"})
	}))
	defer srv.Close()
	api = &client{base: srv.URL, http: srv.Client(), getenv: func(string) string { return "" }}
	code := 0
	oldExit := exitProcess
	exitProcess = func(n int) { code = n }
	defer func() { exitProcess = oldExit }()
	out, err := run(t, "janitor", "scope")
	if err != nil || code != 0 || !strings.Contains(out, "in scope") {
		t.Fatalf("pass: %s %v (%d)", out, err, code)
	}
	answer = w.published
	out, err = run(t, "janitor", "scope")
	if err == nil || !strings.Contains(err.Error(), "not "+w.unpublished) || strings.Contains(out, "in scope") {
		t.Fatalf("wrong commit shown: %s %v", out, err)
	}
	answer, status = w.unpublished, "error"
	out, err = run(t, "janitor", "scope")
	if err == nil || !strings.Contains(err.Error(), "judge unavailable") || strings.Contains(out, "in scope") {
		t.Fatalf("error called a verdict: %s %v", out, err)
	}
}

func TestJanitorScopeSendsScratchBeforeJudgingAndPrintsTheReason(t *testing.T) {
	w := newCheckWorld(t, nil)
	code := 0
	oldExit := exitProcess
	exitProcess = func(n int) { code = n }
	t.Cleanup(func() { exitProcess = oldExit })
	head := w.unpublished // a commit AppTown does not have until it is sent
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
			httpapi.WriteFault(rw, &verification.CommitNotOnAppTownError{Commit: fmt.Sprint(in["commit"])})
			return
		}
		httpapi.WriteJSON(rw, 200, map[string]any{"commit": head, "status": "fail", "brief": "Approved level: routine", "reason": "Modifies REPORT.md"})
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

	out, err := run(t, "janitor", "scope", "-run", "00000000-0007-7000-8000-000000000000")
	if err != nil || code != 1 || sent() != head || (*w.bodies)[0]["commit"] != head {
		t.Fatalf("the step's HEAD was not sent then checked: %v, ref holds %q, asked %v\n%s", err, sent(), *w.bodies, out)
	}
	if !strings.Contains(out, "Approved level: routine") || !strings.Contains(out, "outside your settings: Modifies REPORT.md") {
		t.Fatalf("missing feedback: %s", out)
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
