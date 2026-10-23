package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/mcpapi"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

type previewTransportHost struct {
	calls  int
	result preview.Result
}

func (h *previewTransportHost) BootPreview(context.Context, preview.Change, bool) (preview.Result, error) {
	h.calls++
	return h.result, nil
}

// The provider algorithms retain their existing boot tests. This fixture checks
// the user doors with real domain-created accounts, grants and change records.
func TestPreviewCommandChecksCurrentAuthorityAndTransports(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var projectID ids.ID
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		projectID, err = project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "preview", Name: "Preview"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	host := &previewTransportHost{result: preview.Result{URL: "http://preview", HostPort: 12345, Revision: revision}}
	deps := previewDependenciesForTest(a)
	deps.Host = host
	a.Preview = preview.New(a.DB, deps)
	rebuildConsoleOwners(a)

	ctx := context.Background()
	boss, err := a.Access.Bootstrap(ctx, identity.NewAccount{Username: "boss", Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "outsider"} {
		if _, err := a.Access.EnsureUser(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, cap := range []access.Capability{access.CapProjectRead, access.CapChangePreview} {
		if _, err := a.Access.Grant(ctx, identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(cap), Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectID), By: "boss"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := a.Work.Create(a.asAppTown(ctx, projectID, "test"), workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "Checked preview"})
	if err != nil {
		t.Fatal(err)
	}
	selection := preview.Selection{ChangeID: ch.ID, Restart: true}
	owner := access.Principal{Kind: "user", Name: "owner", Via: "session"}
	call := func(pr access.Principal) (preview.Result, error) {
		return a.Preview.RequestChangePreview(verifiedContext(ctx, pr), selection)
	}
	before := host.calls
	for _, pr := range []access.Principal{{}, {Kind: "user", Name: "outsider", Via: "session"}, {Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}} {
		out, err := call(pr)
		if err == nil || out != (preview.Result{}) || host.calls != before {
			t.Fatalf("unauthorized %+v: %+v %v", pr, out, err)
		}
	}
	if _, err := call(access.Principal{}); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatal(err)
	}
	for _, statement := range []string{"-- name: UserByName :one", "-- name: InsertDecision"} {
		dbtest.FailNextStatement(t, a.DB, statement)
		out, err := call(owner)
		if err == nil || out != (preview.Result{}) || host.calls != before {
			t.Fatalf("failed %s: %+v %v", statement, out, err)
		}
	}
	for _, suffix := range []string{"", "/restart"} {
		response := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview%s", ch.ID, suffix), "")
		var out struct {
			URL    string `json:"url"`
			Commit string `json:"commit"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || response.Code != 200 || out.URL == "" || out.Commit != revision[:7] {
			t.Fatalf("HTTP %s: %d %s %v", suffix, response.Code, response.Body.String(), err)
		}
	}
	c := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: "owner", Role: "member", Via: "token"}, "owner via mcp/test")}
	tool := mcpToolsFor(a).ByName("preview_change")
	refused := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: "outsider", Role: "member", Via: "token"}, "outsider via mcp/test")}
	before = host.calls
	if got, err := tool.Run(refused, map[string]any{"id": ch.ID.String()}); err == nil || got != nil || host.calls != before {
		t.Fatalf("unauthorized MCP preview: %+v %v", got, err)
	}
	got, err := tool.Run(c, map[string]any{"id": ch.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	out := got.(map[string]any)
	if out["commit"] != revision || out["previewUrl"] == "" {
		t.Fatalf("MCP source: %+v", out)
	}
	before = host.calls
	dbtest.FailNextStatement(t, a.DB, "-- name: InsertDecision")
	if _, err := tool.Run(c, map[string]any{"id": ch.ID.String()}); err == nil || host.calls != before {
		t.Fatalf("MCP missing decision reached provider: %v", err)
	}

	meta := consoleMetaFor("preview-session", projectID, ch.ID)
	meta.Actor, meta.Token = "owner", "preview-test-session-token"
	session := a.Console.Restore(meta)
	pr := conversation.Principal(session)
	if _, err := a.Preview.RequestChangePreview(verifiedContext(ctx, pr), preview.Selection{ChangeID: idstest.N(999999), Restart: true}); err == nil || host.calls != before {
		t.Fatalf("console escaped its own change: %v", err)
	}
	r := httptest.NewRequest("POST", "/console/preview-session/preview", nil)
	r.SetPathValue("sid", session.ID)
	r.Header.Set("X-AppTown-Session-Token", session.Token)
	response := httptest.NewRecorder()
	httpapi.NewConsolePreview(a.Console, a.Preview, a.DB, a.cfg.publicHost, nil).ConsolePreview(response, r.WithContext(callerContext(r)))
	var consoleOut struct {
		URL, ContainerURL string
		Port              int
	}
	if err := json.Unmarshal(response.Body.Bytes(), &consoleOut); err != nil || response.Code != 200 || consoleOut.Port != host.result.HostPort || !strings.Contains(consoleOut.ContainerURL, fmt.Sprint(host.result.HostPort)) {
		t.Fatalf("console: %d %s %v", response.Code, response.Body.String(), err)
	}
	// With a preview domain the chat is given that address: the port is
	// published on the host's loopback, which its container cannot reach.
	domained := environmentDependenciesFor(a)
	domained.Routes.PreviewScheme, domained.Routes.PreviewDomain = "https", "preview.example"
	deps.Environment = deployment.NewEnvironment(deps.Targets, domained)
	a.Preview = preview.New(a.DB, deps)
	rebuildConsoleOwners(a)
	response = httptest.NewRecorder()
	httpapi.NewConsolePreview(a.Console, a.Preview, a.DB, a.cfg.publicHost, nil).ConsolePreview(response, r.WithContext(callerContext(r)))
	if err := json.Unmarshal(response.Body.Bytes(), &consoleOut); err != nil || response.Code != 200 || consoleOut.ContainerURL != consoleOut.URL {
		t.Fatalf("console with a preview domain: %d %s %v", response.Code, response.Body.String(), err)
	}
	before = host.calls
	disabled := true
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, boss.Principal); err != nil {
		t.Fatal(err)
	}
	for _, pr := range []access.Principal{owner, conversation.Principal(session)} {
		if out, err := call(pr); err == nil || out != (preview.Result{}) || host.calls != before {
			t.Fatalf("disabled principal reached provider: %+v %v", out, err)
		}
	}
}
