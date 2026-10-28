package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/poll"
)

// AppTown reports its own failures through its own attachment: with
// AppTown project attached to error reporting, an errored step becomes
// one Sentry event at the DSN's store endpoint, tagged by run and step;
// unattached, nothing is sent.
func TestAppTownReportsItsOwnErrorsThroughItsAttachment(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var mu sync.Mutex
	var got []map[string]any
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var ev map[string]any
		_ = json.Unmarshal(b, &ev)
		mu.Lock()
		got = append(got, ev)
		auth = append(auth, r.Header.Get("X-Sentry-Auth"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	a.selfErr.client = srv.Client()
	a.selfErr.storeURL = func(d sentryDSN) string { return srv.URL + "/api/" + d.Project + "/store/" }

	// Unattached: nothing is sent. The reporter decides before any send:
	// an event it sends is first counted in its minute's burst, and only
	// then goes out on its own goroutine, so a count of none is none sent.
	admitted := func() int { a.selfErr.mu.Lock(); defer a.selfErr.mu.Unlock(); return a.selfErr.sent }
	selfErrorsFor(a).Report("panic", "RunAgent", "boom", nil, "stack")
	if n := admitted(); n != 0 {
		t.Fatalf("nothing must be sent while the self-reporting project (apptown) is unattached: %d sent", n)
	}

	// The project a person attaches is the one the guide names: "apptown".
	pid, _, _ := fleetRepo(t, a, "apptown", map[string]string{"README.md": "# apptown"})
	if err := integration.NewAttachments(a.DB).Attach(t.Context(), pid, integration.CapErrors, "glitchtip", `{"dsn":"https://abc123@errors.example/7","providerProject":"forge"}`, integration.Provisioned, "fixture", map[string]any{"providerProject": "forge"}); err != nil {
		t.Fatal(err)
	}
	a.selfErr.checked = time.Time{} // re-read the attachment
	if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "self-errors workflow fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	// Admit the run through Work so the error report observes the same durable
	// run and step records as production execution.
	defineWorkflows(t, a, workflow.PreparedRun{
		Workflow: workflow.DefinitionRef{Name: "self-errors", Version: 1},
		Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
			{Name: "boot", Uses: "preview"},
			{Name: "tests", Uses: "tests"},
		}}}},
	})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Git = fakeGit{}
		deps.Runners = map[string]workflow.Runner{
			"preview": func(task *workflow.Execution) {
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				if err := task.Finish(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "the app was not running when the review looked"}, nil)); err != nil {
					t.Error(err)
				}
			},
			"tests": func(task *workflow.Execution) {
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				if err := task.Finish(workflow.EncodeOutcome(workflow.StatusPass, map[string]any{}, nil)); err != nil {
					t.Error(err)
				}
			},
		}
	})
	a.Work.Stop()
	change, err := a.Work.Create(a.asEngine(context.Background()), workflow.ChangeDraft{ProjectID: pid, Title: "self-errors"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(a.asEngine(context.Background()), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "self-errors"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(a.asEngine(context.Background()), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("admitted steps: %+v %v", snapshot, err)
	}
	stepID := snapshot.Steps[0].ID
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	poll.Until(context.Background(), 3*time.Second, 10*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	if len(got) != 1 {
		t.Fatalf("want one event, got %d", len(got))
	}
	ev := got[0]
	tags, _ := ev["tags"].(map[string]any)
	if tags["kind"] != "step-error" || tags["where"] != "preview/boot" || tags["step"] == nil {
		t.Fatalf("tags: %v", tags)
	}
	if !strings.Contains(auth[0], "sentry_key=abc123") {
		t.Fatalf("auth header names the DSN key: %s", auth[0])
	}
	if ev["release"] == "" || ev["platform"] != "go" {
		t.Fatalf("event: %v", ev)
	}
	// A pass sends nothing.
	step2 := snapshot.Steps[1].ID
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step2); err != nil {
		t.Fatal(err)
	}
	// The step's verdict is reported as it is recorded, before ExecuteStep
	// returns: the burst still counts the one error, so nothing else went.
	if n := admitted(); n != 1 {
		t.Fatalf("a passing step is not an error: %d sent", n)
	}
	if d, ok := parseSentryDSN("https://k@errors.example.com/12"); !ok || d.storeURL() != "https://errors.example.com/api/12/store/" {
		t.Fatalf("dsn: %+v %v", d, ok)
	}
	if _, ok := parseSentryDSN("not a dsn"); ok {
		t.Fatal("garbage is not a DSN")
	}
}

// The web UI asks AppTown for its DSN and release; unattached, it gets
// nothing and reports nothing.
func TestBrowserErrorsFollowTheAttachment(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	h := a.Handler()
	get := func() string {
		req := httptest.NewRequest("GET", "/api/v1/errors/browser", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Body.String()
	}
	if b := get(); strings.Contains(b, "dsn") {
		t.Fatalf("unattached: %s", b)
	}
	pid, _, _ := fleetRepo(t, a, selfErrorsProject, map[string]string{"README.md": "# apptown"})
	if err := integration.NewAttachments(a.DB).Attach(t.Context(), pid, integration.CapErrors, "glitchtip", `{"dsn":"https://abc123@errors.example/7","providerProject":"forge"}`, integration.Provisioned, "fixture", map[string]any{"providerProject": "forge"}); err != nil {
		t.Fatal(err)
	}
	a.selfErr.checked = time.Time{}
	if b := get(); !strings.Contains(b, `"dsn":"https://abc123@errors.example/7"`) || !strings.Contains(b, `"release"`) {
		t.Fatalf("attached: %s", b)
	}
	policy := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		return w.Header().Get("Content-Security-Policy")
	}
	original := policy()
	if !strings.Contains(original, " https://errors.example;") || strings.Contains(original, "abc123") {
		t.Fatalf("configured reporting CSP: %s", original)
	}
	// The existing attachment cache controls both the DSN response and a newly
	// served document's CSP. A served document cannot change its header later.
	if err := a.Integrations.Attachments.Attach(t.Context(), pid, integration.CapErrors, "glitchtip", `{"dsn":"https://changed@new-errors.example/8"}`, integration.Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	a.selfErr.checked = time.Time{}
	if b := get(); !strings.Contains(b, `"dsn":"https://changed@new-errors.example/8"`) {
		t.Fatal(b)
	}
	refreshed := policy()
	if !strings.Contains(refreshed, " https://new-errors.example;") || strings.Contains(refreshed, " https://errors.example;") {
		t.Fatalf("refresh retained old reporting origin: %s", refreshed)
	}
	if err := a.Integrations.Attachments.Attach(t.Context(), pid, integration.CapErrors, "glitchtip", `{"dsn":"ftp://key@new-errors.example/8"}`, integration.Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	a.selfErr.checked = time.Time{}
	if b := get(); strings.Contains(b, "dsn") {
		t.Fatalf("invalid browser DSN published: %s", b)
	}
	if strings.Contains(policy(), "new-errors.example") {
		t.Fatal("invalid browser DSN widened CSP")
	}
}

func TestBrowserDiagnosticDestination(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ dsn, origin string }{
		{"https://key@ERRORS.example:8443/prefix/7", "https://errors.example:8443"},
		{"http://key@127.0.0.1:9000/7", "http://127.0.0.1:9000"},
		{"https://key@[::1]:8443/7", "https://[::1]:8443"},
		{"ftp://key@errors.example/7", ""},
		{"https://key@*.example/7", ""},
		{"https://key@errors.example:99999/7", ""},
		{"https://key@errors.example/not-a-project", ""},
	} {
		d, ok := parseSentryDSN(tc.dsn)
		if !ok {
			t.Fatalf("fixture is not parseable: %s", tc.dsn)
		}
		value, origin, valid := d.browserDestination()
		if origin != tc.origin || valid != (tc.origin != "") {
			t.Fatalf("%s: %q %v", tc.dsn, origin, valid)
		}
		if valid && (value == "" || strings.Contains(origin, "key")) {
			t.Fatalf("credentials leaked or missing DSN: %q %q", value, origin)
		}
	}
}
