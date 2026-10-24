package apptown

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gate"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/stamp"
	"github.com/flokkit/apptown/internal/wiregen"
)

func TestRouteTableRejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()
	handler := func(http.ResponseWriter, *http.Request) { t.Fatal("construction invoked a handler") }
	valid := publicRoute("GET /valid", handler, "test")
	cases := map[string][]routeSpec{
		"missing pattern":            {publicRoute("", handler, "test")},
		"missing handler":            {publicRoute("GET /bad", nil, "test")},
		"missing policy":             {{pattern: "GET /bad", handler: handler}},
		"missing capability":         {requireRoute("GET /bad", handler, "", "instance")},
		"unknown capability":         {requireRoute("GET /bad", handler, "unknown", "instance")},
		"unknown resolver":           {requireRoute("GET /bad", handler, access.CapProjectRead, "unknown")},
		"instance membership":        {memberRoute("GET /bad", handler, "instance")},
		"missing exception contract": {ownerRoute("GET /bad", handler, "")},
		"duplicate":                  {valid},
		"equivalent wildcard":        {publicRoute("GET /things/{id}", handler, "test"), publicRoute("GET /things/{name}", handler, "test")},
		"ambiguous":                  {publicRoute("GET /a/{x}/c", handler, "test"), publicRoute("GET /a/b/{y}", handler, "test")},
		"invalid pattern":            {publicRoute("GET /{broken", handler, "test")},
	}

	for name, routes := range cases {
		t.Run(name, func(t *testing.T) {
			mux, err := newRouteMux(nil, append([]routeSpec{valid}, routes...))
			if err == nil || mux != nil {
				t.Fatalf("invalid table exposed mux=%v, err=%v", mux, err)
			}
		})
	}
}

func TestRouteTableCapturesEachDeclaration(t *testing.T) {
	t.Parallel()
	routes := []routeSpec{
		publicRoute("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.PathValue("id"))) }, "test"),
		publicRoute("GET /things/special", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }, "test"),
	}
	first, err := newRouteMux(nil, routes)
	if err != nil {
		t.Fatal(err)
	}
	// Later wiring changes cannot change a router already serving requests.
	routes[0].handler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusGone) }
	second, err := newRouteMux(nil, routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/things/42", 200, "42"},
		{"HEAD", "/things/special", 204, ""},
		{"POST", "/things/42", 405, "Method Not Allowed\n"},
	} {
		w := httptest.NewRecorder()
		first.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Errorf("%s %s: %d %q", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	second.ServeHTTP(w, httptest.NewRequest("GET", "/things/42", nil))
	if w.Code != http.StatusGone {
		t.Fatalf("second router used first handler: %d", w.Code)
	}
}

func TestRouteTableConcurrentHandlerConstruction(t *testing.T) {
	t.Parallel()
	// This exercises actual App wiring. The old global registration inventory
	// raced even when each goroutine assembled a different App.
	// Constructor and SPA routing do not write application data. Keep distinct
	// Apps, but avoid eight authorization setups and database clones for this
	// one concurrent construction scenario.
	db := testDB(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			a := constructApp(t, db, Config{DataDir: t.TempDir()})
			t.Cleanup(func() {
				if err := a.ShutdownBackground(context.Background()); err != nil {
					t.Error(err)
				}
			})
			h := a.Handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != http.StatusOK {
				t.Errorf("SPA status = %d", w.Code)
			}
		})
	}
	wg.Wait()
}

func TestRouteTablePreservesOuterAndProtocolAuthentication(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "internal", AuthHeader: "", AuthProxySecret: ""})

	h := a.Handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/.well-known/oauth-authorization-server", 200},
		{"GET", "/api/v1/model-catalog", 401},
		{"POST", mcpPath, 401},
		{"GET", "/git/game/info/refs?service=git-upload-pack", 401},
		{"GET", "/", 200},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}

	w := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/no-such-endpoint", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown API got %d: %s", w.Code, w.Body.String())
	}
}

func TestRouteTableMissingProjectIsNotFound(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	h := a.Handler()
	for _, path := range []string{
		"POST /api/v1/projects/missing/members",
		"POST /api/v1/projects/missing/deployments/main/backups/1/restore",
	} {
		method, path, _ := strings.Cut(path, " ")
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-AppTown-Actor", "outsider")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: got %d: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestRouteTableInstrumentsRemainScoped(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := plainTestProject(t, a.DB, "game", "Game")
	a.Console.Restore(consoleMetaFor("own", pid, ids.Nil))
	a.Console.Restore(consoleMetaFor("other", pid, ids.Nil))
	testAccounts(t, a.DB, "alice")
	alice := testPrincipalOf(t, a.DB, "alice")
	// Keep the production declarations and replace only the final effect. A
	// rejected instrument must never reach that effect.
	var routes []routeSpec
	for _, route := range appRoutes(a) {
		if route.pattern == "GET /api/v1/projects/{slug}/console" || route.pattern == "GET /api/v1/console/{sid}/messages" {
			route.handler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }
			routes = append(routes, route)
		}
	}
	if len(routes) != 2 {
		t.Fatal("expected console routes absent")
	}
	mux, err := newRouteMux(a, routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		via, path string
		projectID ids.ID
		status    int
	}{
		{"run", "/api/v1/projects/game/console", pid, 204},
		{"run", "/api/v1/projects/game/console", idstest.N(999999), 403},
		{"console", "/api/v1/projects/game/console", pid, 204},
		{"console", "/api/v1/projects/game/console", idstest.N(999999), 403},
		{"console", "/api/v1/console/own/messages", pid, 204},
		{"console", "/api/v1/console/other/messages", pid, 403},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		id := &authIdentity{Via: tc.via, ProjectID: tc.projectID, ConsoleID: "own"}
		if tc.via == "console" {
			// A chat acts for an enabled person.
			id.User, id.PrincipalID = "alice via console/own", alice
		}
		r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, id))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s project %d, %s: got %d: %s", tc.via, tc.projectID, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestRouteResolutionFailureNeverInvokesProtectedHandler(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	var routes []routeSpec
	for _, route := range appRoutes(a) {
		if route.pattern == "GET /api/v1/projects/{slug}" || route.pattern == "GET /api/v1/runs/{id}/diff" {
			route.handler = func(http.ResponseWriter, *http.Request) { t.Error("failed resolution reached the handler") }
			routes = append(routes, route)
		}
	}
	mux, err := newRouteMux(a, routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/projects/missing", 404},
		{"/api/v1/runs/not-a-number/diff", 400},
		{"/api/v1/runs/0000000f-423f-7000-8000-000000000000/diff", 404},
	} {
		if w := authzCall(mux, "boss", "GET", tc.path, ""); w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: ProjectBySlug")
	if w := authzCall(mux, "boss", "GET", "/api/v1/projects/game", ""); w.Code != 500 {
		t.Fatalf("failed storage lookup: %d %s", w.Code, w.Body.String())
	}
}

// A run, change or step id that names nothing says so in words, not as a
// storage error: `apptown wait` given a change's id, and a deleted project's
// change links, printed "sql: no rows in result set". A deleted project's
// records are removed with it, so the words name that possibility.
func TestAMissingRunIsNotFoundInWords(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	for path, want := range map[string]string{
		"/api/v1/runs/0000000f-423f-7000-8000-000000000000":        "no run 0000000f-423f-7000-8000-000000000000: it does not exist",
		"/api/v1/changes/0000000f-423f-7000-8000-000000000000":     "no change 0000000f-423f-7000-8000-000000000000: it does not exist, or its project was deleted",
		"/api/v1/steps/0000000f-423f-7000-8000-000000000000/brief": "no step 0000000f-423f-7000-8000-000000000000",
	} {
		if w := authzCall(a.Handler(), "boss", "GET", path, ""); w.Code != 404 || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "sql:") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// A change's friendly form, its project's slug and its number there, names
// it wherever its id does; a number the project has not used is not found
// in those words.
func TestAChangeIsFoundByItsFriendlyForm(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/changes/game%231", "")
	var out struct{ Change workflow.Change }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Change.ID != fx.Change || out.Change.Number != 1 {
		t.Fatalf("game#1: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/changes/game%2399", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "no change game#99") {
		t.Fatalf("game#99: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(a.Handler(), "outsider", "POST", "/api/v1/changes/game%231/abandon", `{}`); w.Code != 403 {
		t.Fatalf("an outsider abandoning game#1: %d %s", w.Code, w.Body.String())
	}
}

// A run's friendly form names it wherever its id does. A project's slug is
// fixed (compatibility.md), so a slug#n written down keeps naming the same
// record: a patch that would change the slug is refused.
func TestARunIsFoundByItsFriendlyFormAndItsSlugStays(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	read := func(path string) (int, string) {
		w := authzCall(h, "boss", "GET", path, "")
		var out struct {
			Run    struct{ ID ids.ID }
			Change struct{ ID ids.ID }
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Run.ID.String() + out.Change.ID.String()
	}
	if code, got := read("/api/v1/runs/game%231"); code != 200 || !strings.Contains(got, fx.Run.String()) {
		t.Fatalf("run game#1: %d %s", code, got)
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/runs/game%2399", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "no run game#99") {
		t.Fatalf("run game#99: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "PATCH", "/api/v1/projects/game", `{"slug":"arcade","expectedRevision":0}`); w.Code != 400 {
		t.Fatalf("a slug change: %d %s", w.Code, w.Body.String())
	}
	if code, got := read("/api/v1/changes/game%231"); code != 200 || !strings.Contains(got, fx.Change.String()) {
		t.Fatalf("change game#1: %d %s", code, got)
	}
}

// A link made before 1.0 named a change, run, step, ADR or guidance block
// by the integer it had then; the identity cutover kept each in former_ids.
// The API answers such a number with the record, and its page moves to the
// record's id. Only this AppTown's former identities answer.
func TestALinkMadeBefore1Point0FindsItsRecord(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	w := authzCall(h, "owner", "POST", "/api/v1/projects/game/adrs", `{"title":"a decision","bodyMd":"why"}`)
	var adr struct{ ID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &adr); err != nil || adr.ID.IsZero() {
		t.Fatalf("adr: %d %s", w.Code, w.Body.String())
	}
	block, err := a.Ways.CreateBlock(t.Context(), "house/former", "body", "", "boss")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := a.Access.AppTownID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		instance  ids.ID
		kind, old string
		id        ids.ID
	}{
		{instance, "changes", "41", fx.Change}, {instance, "runs", "42", fx.Run}, {instance, "steps", "43", fx.Accept},
		{instance, "adrs", "44", adr.ID}, {instance, "guidance_blocks", "45", block.ID},
		{idstest.N(77), "changes", "46", fx.Change}, // another AppTown's
	} {
		// The identity cutover wrote former_ids, and it is gone.
		// sql-guarantee: no owner operation writes former_ids now.
		if _, err := a.DB.SQL().ExecContext(t.Context(), `INSERT INTO former_ids(forge, kind, old, id) VALUES ($1, $2, $3, $4)`, row.instance, row.kind, row.old, row.id); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{
		"/api/v1/changes/41": fx.Change.String(), "/api/v1/runs/42": fx.Run.String(), "/api/v1/steps/43/question": `"step":"accept"`,
		"/api/v1/adrs/44": adr.ID.String(), "/api/v1/guidance/blocks/45": block.ID.String(),
	} {
		w := authzCall(h, "boss", "GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("GET %s: %d %s; want the record (%s)", path, w.Code, w.Body.String(), want)
		}
	}
	// The old number names the record and grants nothing: each caller gets
	// the answer its id gets.
	for _, who := range []string{"owner", "outsider", "kid"} {
		for old, id := range map[string]ids.ID{"/api/v1/adrs/44": adr.ID, "/api/v1/changes/41": fx.Change} {
			byOld, byID := authzCall(h, who, "GET", old, ""), authzCall(h, who, "GET", strings.Replace(old, path.Base(old), id.String(), 1), "")
			if byOld.Code != byID.Code {
				t.Errorf("%s reading %s: %d, by its id %d", who, old, byOld.Code, byID.Code)
			}
		}
	}
	for _, path := range []string{"/api/v1/changes/46", "/api/v1/changes/47", "/api/v1/runs/41"} {
		if w := authzCall(h, "boss", "GET", path, ""); w.Code != 404 {
			t.Errorf("GET %s: %d %s; want not found", path, w.Code, w.Body.String())
		}
	}
	for path, want := range map[string]string{
		"/changes/41?tab=work&run=42&step=43": "/changes/" + fx.Change.String() + "?run=" + fx.Run.String() + "&step=" + fx.Accept.String() + "&tab=work",
		"/adrs/44":                            "/adrs/" + adr.ID.String(),
		"/guidance/45":                        "/guidance/" + block.ID.String(),
	} {
		w := authzCall(h, "boss", "GET", path, "")
		if w.Code != 301 || w.Header().Get("Location") != want {
			t.Errorf("page %s: %d to %q; want 301 to %s", path, w.Code, w.Header().Get("Location"), want)
		}
	}
	for _, path := range []string{"/changes/46", "/changes/47", "/changes/" + fx.Change.String(), "/changes/game%231"} {
		if w := authzCall(h, "boss", "GET", path, ""); w.Code == 301 {
			t.Errorf("page %s moved to %s; only a former number this AppTown kept moves", path, w.Header().Get("Location"))
		}
	}
}

func TestRouteResolutionHonorsRequestCancellation(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, resolver := range []string{"project-slug", "run"} {
		r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		r.SetPathValue("slug", "game")
		r.SetPathValue("id", fixtureWork(t, a).Run.String())
		if _, _, err := a.resolveRouteResource(r, resolver); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored cancelled request: %v", resolver, err)
		}
	}
}

// A restored copy's gate redeems the ticket of AppTown's own gate check
// through a protocol door outside the API, authenticated by the copy's
// gate secret.
func TestTheGateRedemptionIsAProtocolDoor(t *testing.T) {
	t.Parallel()
	redeem := false
	for _, route := range appRoutes(nil) {
		if route.pattern == "POST "+gate.RedeemPath {
			p, ok := route.policy.(exceptionalPolicy)
			redeem = ok && p.kind == "protocol"
		}
	}
	if !redeem {
		t.Fatal("the redemption door is not a protocol door")
	}
}

// The API description (stock/docs/api/openapi.json) is generated from this
// route table.

var updateAPI = flag.Bool("update-api", false, "rewrite stock/docs/api/openapi.json from the route table")

// apiDescriptionPath is the shipped document, embedded with stock and
// served at GET /api/v1/openapi.json.
var apiDescriptionPath = filepath.Join("..", "..", "stock", "docs", "api", "openapi.json")

// described is what the description says of one operation beyond its
// route: the body it reads and what it answers on success.
type described struct {
	request any
	strict  bool // the known-field DTO refuses unspecified request keys
	// media is the request body's type when it is not JSON; such a body is
	// bytes, not a schema.
	media    string
	query    []string // query parameters the CLI sends
	status   int
	response any
	// also names a second success shape the same status may carry, with
	// when.
	also     any
	alsoWhen string
}

// cliOperations are the routes the core-loop commands call with --json,
// with any of their options (the commands the compatibility guide makes
// stable), with the Go types their handlers decode and write. The CLI's
// own --json output is the stable contract; these shapes are experimental
// like every route, described so a client can read them. The CLI's tests
// check every request its --json commands send is one of these (jsonAppTown
// in cmd/apptown/output_test.go).
var cliOperations = map[string]described{
	"GET /api/v1/projects":                                   {status: 200, response: []httpapi.ProjectRow{}, also: httpapi.ProjectPage{}, alsoWhen: "with limit, offset or q"},
	"POST /api/v1/projects":                                  {request: httpapi.ProjectDraft{}, status: 201, response: project.Project{}},
	"POST /api/v1/projects/{slug}/repo":                      {request: httpapi.RepositoryStart{}, status: 200, response: project.Project{}},
	"POST /api/v1/projects/{slug}/repo/from-url":             {request: httpapi.RepositoryFromURL{}, status: 200, response: project.Project{}},
	"POST /api/v1/projects/{slug}/repo/from-bundle":          {media: "application/x-git-bundle", query: []string{"branch"}, status: 200, response: project.Project{}},
	"POST /api/v1/projects/{slug}/repo/remote":               {request: httpapi.RemoteRequest{}, status: 200, response: httpapi.RemoteAttached{}},
	"GET /api/v1/scm/namespaces":                             {status: 200, response: httpapi.RemoteNamespaces{}},
	"POST /api/v1/projects/{slug}/toolchain":                 {request: httpapi.ToolchainChoice{}, status: 200, response: httpapi.ToolchainWritten{}},
	"POST /api/v1/projects/{slug}/pack":                      {request: httpapi.PackChoice{}, status: 200, response: projectlifecycle.PackApplied{}},
	"GET /api/v1/packs":                                      {status: 200, response: []httpapi.PackView{}},
	"GET /api/v1/projects/{slug}/secrets":                    {status: 200, response: httpapi.ProjectSecrets{}},
	"GET /api/v1/projects/{slug}/board":                      {status: 200, response: httpapi.ChangeBoard{}},
	"POST /api/v1/projects/{slug}/changes":                   {request: httpapi.ChangeRequest{}, status: 201, response: httpapi.CreatedChange{}},
	"GET /api/v1/changes/{id}":                               {status: 200, response: httpapi.ChangeCockpit{}},
	"POST /api/v1/changes/{id}/land":                         {status: 201, response: httpapi.ChangeRun{}},
	"POST /api/v1/changes/{id}/recheck":                      {status: 200, response: httpapi.RecheckStarted{}},
	"GET /api/v1/runs/{id}":                                  {status: 200, response: workflow.RunSnapshot{}},
	"GET /api/v1/steps/{id}/question":                        {status: 200, response: workflow.ApprovalQuestion{}},
	"POST /api/v1/steps/{id}/approve":                        {request: httpapi.StepDecision{}, status: 200, response: workflow.Step{}},
	"POST /api/v1/projects/{slug}/release":                   {request: httpapi.ReleaseRequest{}, status: 202, response: deployment.LANReleaseStarted{}},
	"POST /api/v1/projects/{slug}/deployments/{name}/deploy": {request: httpapi.DeployRequest{}, strict: true, status: 202, response: httpapi.ReleaseRequested{}},
	"GET /api/v1/projects/{slug}/releases/{number}":          {status: 200, response: httpapi.ReleaseDetail{}},
	"POST /api/v1/complaints":                                {request: httpapi.ComplaintText{}, status: 201, response: httpapi.ComplaintFiled{}},
	"GET /api/v1/operations/{id}":                            {status: 200, response: httpapi.Operation{}},
	"GET /api/v1/setup":                                      {status: 200, response: httpapi.SetupState{}},
}

// describedOperations add full schemas at existing non-CLI API doors. They
// share reflection with CLI descriptions, without claiming CLI usage.
var describedOperations = map[string]described{
	"POST /api/v1/projects/{slug}/deployments": {request: httpapi.DeploymentConfigurationRequest{}, strict: true, status: 200, response: deployment.Deployment{}},
	"GET /api/v1/projects/{slug}/deployments":  {status: 200, response: []httpapi.DeploymentView{}},
}

// failureResult is a handler that answers a failure with its own result
// instead of the refusal shape: the record of what failed is the answer.
// Every JSON write whose status is not a 2xx constant is one of these, the
// refusal writer, or a forward (TestEveryFailureBodyIsDescribed).
type failureResult struct {
	handler string // Receiver.Method, or a function's name
	routes  []string
	status  int // 0: the handler writes the success status its callers pass
	body    any
	why     string
}

var failureResults = []failureResult{
	{handler: "writeBackupTaken", routes: []string{"POST /api/v1/projects/{slug}/deployments/{name}/backups"},
		status: 502, body: httpapi.BackupTaken{}, why: "a backup that failed is recorded; the answer is that record, ok false"},
	{handler: "ProjectDeletion.answer", status: 0, why: "writes the 200 or 202 its callers name; not a failure"},
}

// notFailureResults are the other functions that write JSON with a status
// they are given: the refusal writer itself, forwards to it, and OAuth's
// error writer, whose shape RFC 6749 fixes.
var notFailureResults = map[string]string{
	"WriteError": "the refusal shape, Error",
	"writeJSON":  "a forward to httpapi.WriteJSON; its callers are scanned",
	"oauthError": "OAuth's {error, error_description} (RFC 6749), on the OAuth protocol routes",
}

// The shipped API description is generated from the route table: every
// route, its access rule, experimental, the refusal shape, the core loop's
// request and response schemas, and the failure results. A route or type
// change fails here until the document is regenerated.
func TestAPIDescriptionIsCurrent(t *testing.T) {
	doc, err := apiDescription()
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *updateAPI {
		if err := os.WriteFile(apiDescriptionPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(apiDescriptionPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("stock/docs/api/openapi.json is stale; run: go test ./internal/apptown -run TestAPIDescriptionIsCurrent -update-api")
	}
}

// The description is served to a caller with no credential, through the
// assembled handler, in each authenticating mode; the routes around it keep
// their authentication.
func TestTheAPIDescriptionNeedsNoCredential(t *testing.T) {
	shipped, err := os.ReadFile(apiDescriptionPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{AuthMode: "internal"}, {AuthMode: "trusted-header", AuthHeader: "X-SSO-User", AuthProxySecret: "hush"}} {
		a := testApp(t, cfg)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/openapi.json", nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || !bytes.Equal(w.Body.Bytes(), shipped) {
			t.Errorf("%s: GET /api/v1/openapi.json answered %d %.80s", cfg.AuthMode, w.Code, w.Body.String())
		}
		for _, req := range []*http.Request{httptest.NewRequest("POST", "/api/v1/openapi.json", nil), httptest.NewRequest("GET", "/api/v1/projects", nil)} {
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, req)
			if w.Code != 401 {
				t.Errorf("%s: %s %s without a credential answered %d", cfg.AuthMode, req.Method, req.URL.Path, w.Code)
			}
		}
	}
}

// Each described operation and failure result names a route the table
// holds, so a renamed route cannot leave its description behind.
func TestDescribedOperationsAreRoutes(t *testing.T) {
	routes := map[string]bool{}
	for _, r := range appRoutes(nil) {
		routes[r.pattern] = true
	}
	for pattern := range cliOperations {
		if !routes[pattern] {
			t.Errorf("described %q, which is not a route", pattern)
		}
	}
	for _, f := range failureResults {
		for _, pattern := range f.routes {
			if !routes[pattern] {
				t.Errorf("%s: failure result for %q, which is not a route", f.handler, pattern)
			}
		}
	}
}

// A request requires no field: encoding/json decodes a body with any field
// missing, and a handler says in words what it needs.
func TestDescribedRequestsRequireNoField(t *testing.T) {
	doc, err := apiDescription()
	if err != nil {
		t.Fatal(err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]map[string]any)
	n := 0
	for pattern, d := range cliOperations {
		if d.request == nil {
			continue
		}
		n++
		name := reflect.TypeOf(d.request).Name()
		if _, ok := schemas[name]["required"]; ok {
			t.Errorf("%s: the request %s requires fields", pattern, name)
		}
	}
	if n == 0 {
		t.Fatal("no request described")
	}
}

func apiDescription() (map[string]any, error) {
	schemas := wiregen.NewSchemas("#/components/schemas/")
	uuid := map[string]any{"type": []any{"string", "null"}, "format": "uuid"}
	when := map[string]any{"type": "string", "format": "date-time"}
	schemas.Map(ids.ID{}, uuid)
	// An optional timestamp: absent is the empty string (stamp.Time).
	schemas.Map(stamp.Time{}, map[string]any{"anyOf": []any{when, map[string]any{"const": ""}}})
	schemas.Map(time.Time{}, when)
	schemas.Map(json.RawMessage{}, map[string]any{})
	runState, err := schemas.Ref(workflow.RunState(0))
	if err != nil {
		return nil, err
	}
	schemas.Map(workflow.OptionalRunState{}, map[string]any{"anyOf": []any{runState, map[string]any{"const": ""}}})
	// Types whose Go names collide take the names the web types give them.
	for _, n := range []wiregen.Named{{Name: "ADRChangeSummary", Value: adrs.ChangeSummary{}}, {Name: "ADRState", Value: adrs.State(0)}, {Name: "ADRRecord", Value: adrs.Record{}}} {
		if _, err := schemas.Named(n.Name, n.Value); err != nil {
			return nil, err
		}
	}
	refusal, err := schemas.Named("Error", struct {
		Error string `json:"error"`
	}{})
	if err != nil {
		return nil, err
	}
	failures := map[string]failureResult{}
	for _, f := range failureResults {
		for _, pattern := range f.routes {
			failures[pattern] = f
		}
	}

	paths := map[string]any{}
	for _, route := range appRoutes(nil) {
		method, path, hasMethod := strings.Cut(route.pattern, " ")
		if !hasMethod {
			method, path = "", route.pattern
		}
		openPath, params := openAPIPath(path)
		item, _ := paths[openPath].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[openPath] = item
		}
		access, protocol, err := describeAccess(route.policy)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", route.pattern, err)
		}
		op := map[string]any{
			"operationId":         operationID(method, path),
			"tags":                []string{tagOf(path)},
			"x-apptown-access":    access,
			"x-apptown-stability": "experimental",
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		responses := map[string]any{}
		if protocol {
			responses["default"] = map[string]any{"description": "this route speaks its protocol (" + access["contract"].(string) + "), not AppTown's JSON"}
		} else {
			responses["default"] = map[string]any{"$ref": "#/components/responses/Refusal"}
			responses["2XX"] = map[string]any{"description": "the operation's result; its shape is not described in 1.0"}
		}
		d, cli := cliOperations[route.pattern]
		if !cli {
			d = describedOperations[route.pattern]
		}
		if d.status != 0 {
			delete(responses, "2XX")
			if cli {
				op["x-apptown-used-by"] = "cli"
			}
			if d.media != "" {
				op["requestBody"] = map[string]any{"required": true, "content": map[string]any{d.media: map[string]any{}}}
			}
			for _, name := range d.query {
				params = append(params, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "string"}})
				op["parameters"] = params
			}
			if d.request != nil {
				ref, err := requestRef(schemas, d.request)
				if err != nil {
					return nil, fmt.Errorf("%s request: %w", route.pattern, err)
				}
				if _, configuration := d.request.(httpapi.DeploymentConfigurationRequest); configuration {
					name := strings.TrimPrefix(ref["$ref"].(string), "#/components/schemas/")
					policy := schemas.Components[name]["properties"].(map[string]any)["deploymentPolicy"].(map[string]any)
					policy["type"] = []string{"string", "null"}
					policy["enum"] = []any{nil, "", deployment.CompatibleUpdate, deployment.ProtectedMaintenance}
					policy["default"] = deployment.CompatibleUpdate
					policy["description"] = "compatible-update names the existing procedure, without compatibility certification or automatic data restore. Omitted, null or empty uses that default. protected-maintenance is recognized but unavailable: 422 with deployment_policy_not_implemented, before configuration effects; other contract-assessment 422 responses can follow saved settings."
				}
				if d.strict {
					name := strings.TrimPrefix(ref["$ref"].(string), "#/components/schemas/")
					schemas.Components[name]["additionalProperties"] = false
				}
				op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": ref}}}
			}
			ref, err := schemas.Ref(d.response)
			if err != nil {
				return nil, fmt.Errorf("%s response: %w", route.pattern, err)
			}
			description := "the operation's result"
			if d.also != nil {
				also, err := schemas.Ref(d.also)
				if err != nil {
					return nil, fmt.Errorf("%s response: %w", route.pattern, err)
				}
				ref = map[string]any{"oneOf": []any{ref, also}}
				description += "; the second form " + d.alsoWhen
			}
			responses[strconv.Itoa(d.status)] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": ref}}}
		}
		if f, ok := failures[route.pattern]; ok && f.status != 0 {
			ref, err := schemas.Ref(f.body)
			if err != nil {
				return nil, fmt.Errorf("%s failure: %w", route.pattern, err)
			}
			responses[strconv.Itoa(f.status)] = map[string]any{"description": f.why, "content": map[string]any{"application/json": map[string]any{"schema": ref}}}
		}
		op["responses"] = responses
		if method == "" {
			// A pattern without a method answers every method: a protocol's
			// door (Git, MCP) or the web app's fallback.
			item["x-apptown-any-method"] = op
			continue
		}
		item[strings.ToLower(method)] = op
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "AppTown HTTP API",
			"version": "v1",
			"description": "Every route an AppTown serves, generated from its route table. " +
				"The HTTP API is experimental in 1.0: routes and shapes may change in a 1.x release, " +
				"and the release notes name each change. The stable surfaces are the CLI's core loop " +
				"(with --json) and the MCP tools; see What 1.0 promises. Request and response shapes " +
				"are described for every route the core-loop commands call with --json, with any of their " +
				"options (x-apptown-used-by: cli), and for " +
				"failures that answer with their own result; other routes answer a refusal as Error.",
		},
		"servers":             []any{map[string]any{"url": "/"}},
		"x-apptown-stability": "experimental",
		"paths":               paths,
		"components": map[string]any{
			"schemas": schemas.Components,
			"headers": map[string]any{
				"X-AppTown-Deny-Code": map[string]any{
					"description": "the refusal's code, when it has one (for example landing-occupied)",
					"schema":      map[string]any{"type": "string"},
				},
			},
			"responses": map[string]any{
				"Refusal": map[string]any{
					"description": "a refusal or failure, in words",
					"headers":     map[string]any{"X-AppTown-Deny-Code": map[string]any{"$ref": "#/components/headers/X-AppTown-Deny-Code"}},
					"content":     map[string]any{"application/json": map[string]any{"schema": refusal}},
				},
			},
		},
	}, nil
}

// requestRef is a request body's schema. encoding/json decodes a body with
// any field missing, so a request requires no field: what a handler needs,
// it refuses in words. A request type that reaches another named type would
// share that type's required fields with its responses, so it is refused.
func requestRef(schemas *wiregen.Schemas, v any) (map[string]any, error) {
	ref, err := schemas.Ref(v)
	if err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(ref["$ref"].(string), "#/components/schemas/")
	body := schemas.Components[name]
	delete(body, "required")
	for prop, schema := range body["properties"].(map[string]any) {
		if strings.Contains(fmt.Sprint(schema), "$ref") {
			return nil, fmt.Errorf("%s.%s reaches a named type; describe it as a request of its own", name, prop)
		}
	}
	return ref, nil
}

var pathParam = regexp.MustCompile(`\{([A-Za-z0-9_]+)(\.\.\.)?\}`)

// openAPIPath is a mux pattern's path as OpenAPI writes it, with its
// parameters. A trailing {name...} takes the rest of the path.
func openAPIPath(path string) (string, []any) {
	var params []any
	out := pathParam.ReplaceAllStringFunc(path, func(m string) string {
		parts := pathParam.FindStringSubmatch(m)
		p := map[string]any{"name": parts[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
		if parts[2] != "" {
			p["description"] = "the rest of the path, which may contain /"
		}
		params = append(params, p)
		return "{" + parts[1] + "}"
	})
	return out, params
}

func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	if method == "" {
		b.WriteString("any")
	}
	for _, word := range strings.FieldsFunc(strings.TrimPrefix(path, "/api/v1"), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return b.String()
}

func tagOf(path string) string {
	rest := strings.TrimPrefix(path, "/api/v1/")
	if rest == path {
		return "outside /api/v1"
	}
	first, _, _ := strings.Cut(rest, "/")
	return first
}

// describeAccess is a route's access rule as the description writes it,
// and whether the route speaks a protocol of its own.
func describeAccess(policy routePolicy) (map[string]any, bool, error) {
	switch p := policy.(type) {
	case requiredPolicy:
		out := map[string]any{"kind": "capability", "capability": string(p.capability), "scope": p.resolver}
		if p.chat {
			out["chat"] = true
		}
		if p.decidedBy != "" {
			out["decidedBy"] = p.decidedBy
		}
		return out, false, nil
	case memberPolicy:
		return map[string]any{"kind": "member", "scope": p.resolver}, false, nil
	case administratorPolicy:
		return map[string]any{"kind": "administrator"}, false, nil
	case exceptionalPolicy:
		return map[string]any{"kind": p.kind, "contract": p.contract}, p.kind == "protocol", nil
	}
	return nil, false, fmt.Errorf("no description for the access rule %T", policy)
}

// Every handler that writes JSON with a status that is not a 2xx constant
// is declared: a failure result (described as its operation's answer), the
// refusal writer, or a forward. A new one fails here until it is declared,
// so the description cannot claim the refusal shape for a body it is not.
func TestEveryFailureBodyIsDescribed(t *testing.T) {
	declared := map[string]bool{}
	for _, f := range failureResults {
		declared[f.handler] = true
	}
	for name := range notFailureResults {
		declared[name] = true
	}
	found := map[string]bool{}
	for _, dir := range []string{".", "httpapi"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) != 3 || !writesJSON(call.Fun) || isSuccessStatus(call.Args[1]) {
						return true
					}
					handler := funcName(fn)
					found[handler] = true
					if !declared[handler] {
						t.Errorf("%s: %s writes JSON with status %s; declare it in failureResults (described with its operation) or route the failure through WriteError",
							fset.Position(call.Pos()), handler, firstLine(call.Args[1]))
					}
					for _, f := range failureResults {
						if lit, ok := call.Args[1].(*ast.BasicLit); ok && f.handler == handler && f.status != 0 && lit.Value != strconv.Itoa(f.status) {
							t.Errorf("%s: %s writes %s, declared %d", fset.Position(call.Pos()), handler, lit.Value, f.status)
						}
					}
					return true
				})
			}
		}
	}
	for name := range declared {
		if !found[name] {
			t.Errorf("%s is declared but writes no such JSON; remove it", name)
		}
	}
}

func writesJSON(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "WriteJSON" || f.Name == "writeJSON"
	case *ast.SelectorExpr:
		return f.Sel.Name == "WriteJSON"
	}
	return false
}

// isSuccessStatus is a status written as a 2xx constant: a literal or one
// of net/http's names.
func isSuccessStatus(e ast.Expr) bool {
	switch s := e.(type) {
	case *ast.BasicLit:
		n, err := strconv.Atoi(s.Value)
		return err == nil && n >= 200 && n < 300
	case *ast.SelectorExpr:
		pkg, ok := s.X.(*ast.Ident)
		return ok && pkg.Name == "http" && slices.Contains([]string{"StatusOK", "StatusCreated", "StatusAccepted", "StatusNoContent"}, s.Sel.Name)
	}
	return false
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func firstLine(e ast.Expr) string {
	var b bytes.Buffer
	_ = ast.Fprint(&b, token.NewFileSet(), e, nil)
	first, _, _ := strings.Cut(strings.TrimSpace(b.String()), "\n")
	return first
}

// AppTown has no backlog: a change is opened when someone starts work on
// it. A request from before the backlog was removed, asking for a proposed
// change, is refused rather than opening an in-progress change the caller
// did not ask for, and there is no start for a change that is already open.
//
// Kills: change creation that ignores an unknown "propose" field, or a
// start route left behind.
func TestTheBacklogIsGone(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	pid := createReadTestProject(t, a, "shop")
	opened := func() int {
		t.Helper()
		list, err := a.Work.Changes(asPerson("bls"), pid, workflow.ChangeList{})
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	if w := authzCall(a.Handler(), "bls", "POST", "/api/v1/projects/shop/changes", `{"type":"feature","title":"Show prices in euros"}`); w.Code != 201 || opened() != 1 {
		t.Fatalf("an ordinary change: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(a.Handler(), "bls", "POST", "/api/v1/projects/shop/changes", `{"type":"feature","title":"An idea for later","propose":true}`); w.Code != 400 || opened() != 1 {
		t.Fatalf("a backlog request: %d %s, %d changes", w.Code, w.Body.String(), opened())
	}
	id := createReadTestChange(t, a, pid, "Open work")
	if w := authzCall(a.Handler(), "bls", "POST", fmt.Sprintf("/api/v1/changes/%s/start", id), ""); w.Code != 404 {
		t.Fatalf("starting an open change: %d %s", w.Code, w.Body.String())
	}
}
