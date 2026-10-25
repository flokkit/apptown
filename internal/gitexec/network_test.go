package gitexec

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/gitlab"
)

func TestVerifiedPushRefusesForeignPushURLsAndRewritesBeforeCredentials(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"foreign pushurl", "multiple pushurls", "rewrite", "credential helper", "HTTP redirect", "verified target"} {
		t.Run(kind, func(t *testing.T) {
			var foreignCalls, credentials atomic.Int64
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignCalls.Add(1)
				http.Error(w, "foreign", http.StatusUnauthorized)
			}))
			defer foreign.Close()
			allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, password, ok := r.BasicAuth(); ok && password == "private-token" {
					credentials.Add(1)
				}
				if kind == "HTTP redirect" {
					http.Redirect(w, r, foreign.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				http.Error(w, "authenticate", http.StatusUnauthorized)
			}))
			defer allowed.Close()
			u, _ := url.Parse(allowed.URL + "/team/p.git")
			u.User = url.User("apptown")
			destination := u.String()
			dir := t.TempDir()
			gitAt(t, dir, "init", "-q", "-b", "main")
			gitAt(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
			gitAt(t, dir, "remote", "add", "origin", destination)
			switch kind {
			case "foreign pushurl":
				gitAt(t, dir, "config", "remote.origin.pushurl", foreign.URL+"/team/p.git")
			case "multiple pushurls":
				gitAt(t, dir, "config", "--add", "remote.origin.pushurl", destination)
				gitAt(t, dir, "config", "--add", "remote.origin.pushurl", foreign.URL+"/team/p.git")
			case "rewrite":
				gitAt(t, dir, "config", "url."+foreign.URL+"/.insteadOf", allowed.URL+"/")
			case "credential helper":
				gitAt(t, dir, "config", "credential.helper", "!echo unexpected")
			}
			client, err := gitlab.New(allowed.URL, "private-token")
			if err != nil {
				t.Fatal(err)
			}
			env, err := client.AskpassEnv(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out, err := PushVerifiedURL(t.Context(), dir, destination, "main", env)
			if err == nil {
				t.Fatalf("unsafe push succeeded: %s", out)
			}
			if kind == "verified target" {
				if credentials.Load() == 0 {
					t.Fatalf("control did not send credential to the verified target: %v %s", err, out)
				}
				return
			}
			if foreignCalls.Load() != 0 || credentials.Load() != 0 {
				t.Fatalf("unsafe destination received request or credentials: foreign=%d credentials=%d", foreignCalls.Load(), credentials.Load())
			}
			if kind != "HTTP redirect" && kind != "verified target" && !strings.Contains(err.Error(), "Git") {
				t.Fatalf("wrong refusal: %v %s", err, out)
			}
		})
	}
}
