package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
)

func TestSCMEnvironmentReusesOnlyProjectScopedCredentials(t *testing.T) {
	t.Parallel()
	var issued atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "owner-secret" {
			t.Error("missing host credential")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/group/app":
			fmt.Fprint(w, `{"id":31,"default_branch":"main"}`)
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/31/protected_branches":
			fmt.Fprint(w, `[{"name":"main","push_access_levels":[{"access_level":40}],"merge_access_levels":[{"access_level":40}]}]`)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/31/access_tokens":
			var request struct {
				Name   string
				Scopes []string
				Level  int `json:"access_level"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Name != "apptown-agent-read" || request.Level != 20 || !reflect.DeepEqual(request.Scopes, []string{"read_api"}) {
				t.Errorf("unexpected grant: %+v", request)
			}
			issued.Add(1)
			fmt.Fprint(w, `{"id":9,"token":"project-secret"}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_URL="+srv.URL+"\nGITLAB_TOKEN=owner-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	service := NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	for range 2 {
		env, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "read")
		if why != "" {
			t.Fatal(why)
		}
		if !reflect.DeepEqual(env, []string{"GITLAB_HOST=" + srv.URL, "GITLAB_TOKEN=project-secret", "GLAB_REPO=group/app"}) {
			t.Fatalf("scope escaped: %v", env)
		}
		if strings.Contains(strings.Join(env, "\n"), "owner-secret") {
			t.Fatal("host credential escaped")
		}
	}
	if issued.Load() != 1 {
		t.Fatalf("minted %d times", issued.Load())
	}
	// The integration resource is a durable cache; reconstructing the owner reuses it.
	service = NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "read"); why != "" || issued.Load() != 1 {
		t.Fatalf("credential reuse: %s / %d", why, issued.Load())
	}
	if env, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "administrator"); env != nil || why == "" {
		t.Fatal("unknown profile granted")
	}
}

// An SCM token's expiry is written and judged by one clock, AppTown's:
// with that clock a week off the wall clock either way, a token just minted
// is reused, and two days later by the same clock (less than a day left of
// its three) it is replaced.
//
// Kills: the expiry written from the wall clock or judged by it (a week
// ahead it reads as expired at once; a week behind it never runs low).
func TestAnSCMTokenIsRotatedByAppTownsClock(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{7 * 24 * time.Hour, -7 * 24 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			t.Parallel()
			var issued atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/group/app":
					fmt.Fprint(w, `{"id":31}`)
				case r.Method == "POST" && r.URL.Path == "/api/v4/projects/31/access_tokens":
					n := issued.Add(1)
					fmt.Fprintf(w, `{"id":%d,"token":"project-secret-%d"}`, n, n)
				case r.Method == "DELETE":
				default:
					t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_URL="+srv.URL+"\nGITLAB_TOKEN=owner-secret\n"), 0600); err != nil {
				t.Fatal(err)
			}
			db := testDB(t)
			id := createTestProject(t, db, "app", "App")
			var ahead atomic.Int64
			ahead.Store(int64(skew))
			service := NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir}), Now: func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }})
			for range 2 {
				if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "read"); why != "" {
					t.Fatal(why)
				}
			}
			if issued.Load() != 1 {
				t.Fatalf("a token just minted was minted %d times", issued.Load())
			}
			ahead.Add(int64(50 * time.Hour))
			if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "read"); why != "" || issued.Load() != 2 {
				t.Fatalf("a token with under a day left was not replaced: %s / %d", why, issued.Load())
			}
		})
	}
}

// scmFake is a fake GitLab that mints access token 9; onMint runs just
// before the mint answers, and revoked collects the token IDs deleted.
func scmFake(t *testing.T, onMint func()) (*httptest.Server, *atomic.Int32, *sync.Map) {
	t.Helper()
	var issued atomic.Int32
	var revoked sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/group/app":
			fmt.Fprint(w, `{"id":31,"default_branch":"main"}`)
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/31/protected_branches":
			fmt.Fprint(w, `[{"name":"main","push_access_levels":[{"access_level":40}],"merge_access_levels":[{"access_level":40}]}]`)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/31/access_tokens":
			issued.Add(1)
			if onMint != nil {
				onMint()
			}
			fmt.Fprint(w, `{"id":9,"token":"project-secret"}`)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v4/projects/31/access_tokens/"):
			revoked.Store(strings.TrimPrefix(r.URL.Path, "/api/v4/projects/31/access_tokens/"), true)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &issued, &revoked
}

func scmService(t *testing.T, db *database.DB, url string) *Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_URL="+url+"\nGITLAB_TOKEN=owner-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
}

// A token GitLab has made is recorded though the caller stops just as it
// is made: AppTown knows the live token, reuses it rather than minting
// another, and the rotation is on the ledger.
func TestAMintedSCMTokenIsRecordedThoughTheCallerStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	srv, issued, _ := scmFake(t, cancel)
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	service := scmService(t, db, srv.URL)
	_, _ = service.SCMEnvironment(ctx, id, "https://gitlab.test/group/app.git", "read")
	res, err := Resource(t.Context(), db, id, CapSCM)
	var recorded struct {
		Profiles map[string]scmToken `json:"profiles"`
	}
	if err != nil || json.Unmarshal([]byte(res), &recorded) != nil || recorded.Profiles["read"].TokenID != 9 {
		t.Fatalf("the minted token was not recorded: %s %v", res, err)
	}
	if _, why := service.SCMEnvironment(context.Background(), id, "https://gitlab.test/group/app.git", "read"); why != "" || issued.Load() != 1 {
		t.Fatalf("the recorded token was not reused: %s, minted %d", why, issued.Load())
	}
	if got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "scm.token-rotated"}); err != nil || len(got) != 1 {
		t.Fatalf("the rotation is not on the ledger: %+v %v", got, err)
	}
}

// A token GitLab has made but AppTown cannot record is revoked at once:
// no live token is left that AppTown does not know.
func TestAnSCMTokenThatCannotBeRecordedIsRevoked(t *testing.T) {
	t.Parallel()
	srv, _, revoked := scmFake(t, nil)
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	service := scmService(t, db, srv.URL)
	t.Cleanup(db.FailNextStatement("-- name: RememberIntegration"))
	if env, why := service.SCMEnvironment(context.Background(), id, "https://gitlab.test/group/app.git", "read"); env != nil || why == "" {
		t.Fatalf("an unrecorded token was handed out: %v", env)
	}
	if _, ok := revoked.Load("9"); !ok {
		t.Fatal("the unrecorded token is still live at GitLab")
	}
}

// The record holds one token per profile, so a rotation whose revocation
// of the old token fails mints nothing: the old token stays recorded (and so
// revocable), and the grant says why. A token GitLab no longer has (404) is
// gone already, and the rotation goes on.
//
// Kills: dropping the revocation's error, which minted over a token GitLab
// still honoured and left a live write token AppTown no longer knew.
func TestARotationMintsNothingWhileTheOldTokenStands(t *testing.T) {
	t.Parallel()
	var issued atomic.Int32
	var deleteStatus atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/group/app":
			fmt.Fprint(w, `{"id":31,"default_branch":"main"}`)
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/31/protected_branches":
			fmt.Fprint(w, `[{"name":"main","push_access_levels":[{"access_level":40}],"merge_access_levels":[{"access_level":40}]}]`)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/31/access_tokens":
			n := issued.Add(1)
			fmt.Fprintf(w, `{"id":%d,"token":"project-secret-%d"}`, n, n)
		case r.Method == "DELETE":
			w.WriteHeader(int(deleteStatus.Load()))
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_URL="+srv.URL+"\nGITLAB_TOKEN=owner-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	var ahead atomic.Int64
	service := NewService(db, Dependencies{Secrets: secret.NewService(db, secret.Config{DataDir: dir}), Now: func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }})
	recorded := func() int64 {
		t.Helper()
		res, err := Resource(t.Context(), db, id, CapSCM)
		var r struct {
			Profiles map[string]scmToken `json:"profiles"`
		}
		if err != nil || json.Unmarshal([]byte(res), &r) != nil {
			t.Fatalf("reading the record: %s %v", res, err)
		}
		return r.Profiles["write"].TokenID
	}
	if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "write"); why != "" || recorded() != 1 {
		t.Fatalf("first token: %s, recorded %d", why, recorded())
	}
	ahead.Store(int64(50 * time.Hour)) // under a day left: due for rotation
	deleteStatus.Store(500)
	if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "write"); why == "" || !strings.Contains(why, "nothing new was minted") {
		t.Fatalf("a failed revocation must refuse the rotation, said %q", why)
	}
	if issued.Load() != 1 || recorded() != 1 {
		t.Fatalf("a token was minted over a live one: issued %d, recorded %d", issued.Load(), recorded())
	}
	deleteStatus.Store(404)
	if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "write"); why != "" || issued.Load() != 2 || recorded() != 2 {
		t.Fatalf("an old token already gone does not hold up the rotation: %s, issued %d, recorded %d", why, issued.Load(), recorded())
	}
}

// A registry provisioning revokes the deploy token it replaces or mints
// nothing, and a recorded attachment that cannot be read mints nothing:
// minting over a token GitLab still honours would leave a live registry
// credential AppTown no longer knows. A token already gone (404) is no
// obstacle.
//
// Kills: a revocation whose answer was dropped, and a predecessor read whose
// failure was taken for "nothing recorded".
func TestARegistryProvisionMintsNothingWhileTheOldTokenStands(t *testing.T) {
	t.Parallel()
	var minted atomic.Int32
	var deleteStatus atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/deploy_tokens/"):
			w.WriteHeader(int(deleteStatus.Load()))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy_tokens"):
			n := minted.Add(1)
			fmt.Fprintf(w, `{"id":%d,"username":"robot-%d","token":"deploy-secret-%d"}`, 40+n, n, n)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	service := scmService(t, db, srv.URL)
	if _, err := service.Bindings.Upsert(t.Context(), "gitlab-registry", CapContainerRegistry, "gitlab", `{"provider":"gitlab","url":"registry.gitlab.example","group":"team"}`, false); err != nil {
		t.Fatal(err)
	}
	p, err := project.New(db, nil).ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	provision := func() error {
		_, _, err := service.ProvisionRegistry(t.Context(), &p, "gitlab-registry", "bls")
		return err
	}
	if err := service.Attachments.Remember(t.Context(), id, CapContainerRegistry, "gitlab-registry", `{"token_id":7,"repo":"registry.gitlab.example/team/app"}`); err != nil {
		t.Fatal(err)
	}
	deleteStatus.Store(500)
	if err := provision(); err == nil || !strings.Contains(err.Error(), "nothing new was minted") || minted.Load() != 0 {
		t.Fatalf("a failed revocation must refuse: %v, minted %d", err, minted.Load())
	}
	if err := service.Attachments.Remember(t.Context(), id, CapContainerRegistry, "gitlab-registry", `["not","an","object"]`); err != nil {
		t.Fatal(err)
	}
	if err := provision(); err == nil || !strings.Contains(err.Error(), "could not be read") || minted.Load() != 0 {
		t.Fatalf("an unreadable attachment must refuse: %v, minted %d", err, minted.Load())
	}
	if err := service.Attachments.Remember(t.Context(), id, CapContainerRegistry, "gitlab-registry", `{"token_id":7,"repo":"registry.gitlab.example/team/app"}`); err != nil {
		t.Fatal(err)
	}
	deleteStatus.Store(404)
	if err := provision(); err != nil || minted.Load() != 1 {
		t.Fatalf("an old token already gone does not hold up provisioning: %v, minted %d", err, minted.Load())
	}
}

// The recorded SCM tokens are what a rotation revokes: a record that cannot
// be read mints nothing.
func TestAnSCMRecordThatCannotBeReadMintsNothing(t *testing.T) {
	t.Parallel()
	srv, issued, _ := scmFake(t, nil)
	db := testDB(t)
	id := createTestProject(t, db, "app", "App")
	service := scmService(t, db, srv.URL)
	if err := service.Attachments.Remember(t.Context(), id, CapSCM, "", `{"profiles":"not a map"}`); err != nil {
		t.Fatal(err)
	}
	if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "write"); !strings.Contains(why, "nothing new was minted") || issued.Load() != 0 {
		t.Fatalf("an unreadable record must refuse: %q, minted %d", why, issued.Load())
	}
}

// A write token is Developer level. GitLab applies every protection rule
// matching a branch, the most permissive winning, and merging is a
// permission apart from pushing: no write token is minted unless at least
// one rule matches the default branch and every matching rule's push and
// merge entries are Maintainer or higher, or No one. Named users, groups and
// deploy keys are refused, not judged. A read token never asks.
//
// Kills: minting whatever the protection; judging one exact rule while a
// wildcard lets Developers push; ignoring who may merge.
func TestAWriteTokenNeedsAProtectedDefaultBranch(t *testing.T) {
	t.Parallel()
	rule := func(name, push, merge string) string {
		return `{"name":"` + name + `","push_access_levels":[` + push + `],"merge_access_levels":[` + merge + `]}`
	}
	const maint, dev, none, user = `{"access_level":40}`, `{"access_level":30}`, `{"access_level":0}`, `{"access_level":null,"user_id":5}`
	for _, tc := range []struct {
		name, rules string
		mints       bool
		why         string
	}{
		{"not protected", `[]`, false, "is not protected"},
		{"another branch only", `[` + rule("release/*", maint, maint) + `]`, false, "is not protected"},
		{"developers may push", `[` + rule("main", dev, maint) + `]`, false, "lets Developers push"},
		{"developers may merge", `[` + rule("main", maint, dev) + `]`, false, "lets Developers merge"},
		{"a wildcard lets developers push", `[` + rule("main", maint, maint) + `,` + rule("*", dev, maint) + `]`, false, "lets Developers push"},
		{"a named user may push", `[` + rule("main", user, maint) + `]`, false, "named user, group or deploy key"},
		{"maintainers only", `[` + rule("main", maint, maint) + `]`, true, ""},
		{"no one pushes, maintainers merge", `[` + rule("main", none, maint) + `]`, true, ""},
		{"a wildcard for maintainers", `[` + rule("ma*", maint, maint) + `]`, true, ""},
		{"a full page may hide a rule", `[` + strings.TrimSuffix(strings.Repeat(rule("main", maint, maint)+`,`, protectionRulesRead), `,`) + `]`, false, "more than AppTown reads in one page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var issued, asked atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/group/app":
					fmt.Fprint(w, `{"id":31,"default_branch":"main"}`)
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/31/protected_branches":
					asked.Add(1)
					fmt.Fprint(w, tc.rules)
				case r.Method == "POST" && r.URL.Path == "/api/v4/projects/31/access_tokens":
					issued.Add(1)
					fmt.Fprint(w, `{"id":9,"token":"project-secret"}`)
				default:
					t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			db := testDB(t)
			id := createTestProject(t, db, "app", "App")
			service := scmService(t, db, srv.URL)
			_, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "write")
			if tc.mints != (why == "") || (!tc.mints && (!strings.Contains(why, tc.why) || issued.Load() != 0)) {
				t.Fatalf("write mint: %q, issued %d", why, issued.Load())
			}
			before := asked.Load()
			if _, why := service.SCMEnvironment(t.Context(), id, "https://gitlab.test/group/app.git", "read"); why != "" || asked.Load() != before {
				t.Fatalf("a read token is unaffected: %q, protection asked %d more times", why, asked.Load()-before)
			}
		})
	}
}

func TestBranchRuleMatchesExactlyOrByWildcard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rule, branch string
		want         bool
	}{{"main", "main", true}, {"main", "mainline", false}, {"*", "main", true}, {"ma*", "main", true}, {"*in", "main", true},
		{"release/*", "main", false}, {"m*n", "main", true}, {"m*x", "main", false}} {
		if got := branchRuleMatches(tc.rule, tc.branch); got != tc.want {
			t.Errorf("%q matches %q: %v, want %v", tc.rule, tc.branch, got, tc.want)
		}
	}
}
