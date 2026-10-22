package apptown

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conversation"

	"github.com/flokkit/apptown/internal/apptown/httpapi"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestInferenceLifecycleAndTrustedReceipts(t *testing.T) {
	t.Parallel()
	a, ctx, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "work", Uses: "agent/pi"}}}}})
	var token string
	var attempt int64
	calls := 0
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Report = func(err error) {
			var verdict *workflow.StepError
			if !errors.As(err, &verdict) {
				t.Error(err)
			}
		}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			claim, err := task.Claim()
			if err != nil {
				t.Fatal(err)
			}
			calls++
			if calls == 1 {
				attempt = claim.Attempt
				token = a.runTok.Step(claim.ID, attempt)
				who, err := inferenceIdentityFor(a).Authenticate(ctx, token)
				if err != nil || who.Actor != "alice" || who.ProjectID != run.ProjectID {
					t.Fatalf("identity %+v %v", who, err)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture-key" {
						t.Error("wrong upstream identity")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
				}))
				defer upstream.Close()
				if _, err := a.Integrations.Bindings.Save(ctx, "paid", "model", "pi", mustJSON(map[string]any{"baseUrl": upstream.URL, "model": "paid/model", "providerDef": map[string]any{"apiKey": "fixture-key"}}), "fixture"); err != nil {
					t.Fatal(err)
				}
				gateway := inference.New([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, inferenceIdentityFor(a), a.providers, a.Usage.Requests)
				request := httptest.NewRequest("POST", "/providers/paid/chat/completions", strings.NewReader(`{"model":"model"}`))
				request.RemoteAddr = "192.0.2.1:1000"
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				gateway.ServeHTTP(response, request)
				if response.Code != 200 {
					t.Fatalf("actual inference: %d %s", response.Code, response.Body.String())
				}
				var actor, outcome, raw string
				// sql-guarantee: inference request receipts have no owner read (usage only writes them); adding one would be test-only API.
				if err := a.DB.QueryRow(`SELECT actor,outcome,usage_json FROM inference_requests WHERE provider=?`, "paid").Scan(&actor, &outcome, &raw); err != nil || actor != "alice" || outcome != "complete" || !strings.Contains(raw, `"input": 8`) {
					t.Fatalf("bad receipt %s %s %s %v", actor, outcome, raw, err)
				}
				// An AppTown with another key (another data directory) does
				// not recognise this attempt's token.
				elsewhere := newApp(t, a.DB)
				if _, err := inferenceIdentityFor(elsewhere).Authenticate(ctx, token); err == nil {
					t.Fatal("step credential admitted under another key")
				}

				if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusError}, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				if claim.Attempt != attempt+1 {
					t.Fatalf("next attempt: %+v", claim)
				}
				if _, err := inferenceIdentityFor(a).Authenticate(ctx, token); err == nil {
					t.Fatal("old attempt admitted")
				}
				if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
					t.Fatal(err)
				}
			}
		}}
	})
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("run steps: %+v %v", snapshot, err)
	}
	stepID := snapshot.Steps[0].ID
	if err := a.Work.ExecuteStep(ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.RetryStep(ctx, workflow.RetryCommand{StepID: stepID}); err != nil {
		t.Fatal(err)
	}
	if err := a.Work.ExecuteStep(ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("runner calls: %d", calls)
	}
	if _, err := inferenceIdentityFor(a).AuthenticateStep(ctx, token); err == nil {
		t.Fatal("a settled attempt kept API access")
	}
	// A chat alice opened has inference until her account is disabled.
	m := consoleMetaFor("alice-chat", run.ProjectID, ids.Nil)
	m.Actor, m.Token = "alice", "alice-chat-secret"
	m.Labels[conversation.OpenerLabel] = testPrincipalOf(t, a.DB, "alice").String()
	chat := a.Console.Restore(m).Token
	if _, err := inferenceIdentityFor(a).AuthenticateStep(ctx, chat); err == nil {
		t.Fatal("a chat's token acquired run API authority")
	}
	if _, err := inferenceIdentityFor(a).Authenticate(ctx, chat); err != nil {
		t.Fatalf("alice's chat was refused before her account was disabled: %v", err)
	}
	testAdministrator(t, a, "admin")
	disabled := true
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "alice", Disabled: &disabled}, access.Principal{Kind: "user", Name: "admin", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := inferenceIdentityFor(a).Authenticate(ctx, chat); err == nil {
		t.Fatal("disabled user still has inference")
	}
}
func TestInferenceChatSurvivesIdleAndRecoveryButNotEnd(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{ConsoleAgent: consoletest.NewAgent()})
	m := consoleMetaFor("long-chat", idstest.N(7), ids.Nil)
	m.Actor, m.Token = "alice", "chat-secret"
	testAccounts(t, a.DB, "alice")
	m.Labels[conversation.OpenerLabel] = testPrincipalOf(t, a.DB, "alice").String()
	m.Labels[conversation.RunLabel], m.Labels[conversation.StepLabel], m.Labels[conversation.AttemptLabel] = idstest.N(42).String(), idstest.N(9).String(), "3"
	s := a.Console.Restore(m)
	who, err := inferenceIdentityFor(a).Authenticate(context.Background(), s.Token)
	if err != nil || who.RunID != idstest.N(42) || who.Actor != "alice" {
		t.Fatalf("continued chat lost identity: %+v %v", who, err)
	}
	// Recovery restores the durable console identity, independently of the old
	// run's state. No time-based lease to renew during a long idle interval.
	restarted := constructApp(t, a.DB, Config{DataDir: t.TempDir(), ConsoleAgent: consoletest.NewAgent()})
	t.Cleanup(func() {
		if err := restarted.ShutdownBackground(context.Background()); err != nil {
			t.Errorf("stopping restarted app: %v", err)
		}
	})
	restarted.Console.Restore(s.Meta)
	if _, err := inferenceIdentityFor(restarted).Authenticate(context.Background(), s.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Console.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inferenceIdentityFor(a).Authenticate(context.Background(), s.Token); err == nil {
		t.Fatal("ended chat admitted")
	}
	if _, err := restarted.Console.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inferenceIdentityFor(restarted).Authenticate(context.Background(), s.Token); err == nil {
		t.Fatal("removed chat admitted")
	}
}
func TestInferenceRecoveryPreservesCompletedReceiptsAndConversationFiles(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	who := usage.Request{Kind: "console", Actor: "alice", Provider: "paid", Model: "model"}
	openID, err := a.Usage.Requests.Begin(context.Background(), who)
	if err != nil {
		t.Fatal(err)
	}
	var originalUsage string
	// sql-guarantee: inference request receipts have no owner read (usage only writes them); adding one would be test-only API.
	if err := a.DB.QueryRow(`SELECT usage_json FROM inference_requests WHERE id=?`, openID).Scan(&originalUsage); err != nil {
		t.Fatal(err)
	}
	closedID, err := a.Usage.Requests.Begin(context.Background(), who)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Usage.Requests.Finish(context.Background(), closedID, 200, "complete", `{"observed":true,"input":8,"output":3}`); err != nil {
		t.Fatal(err)
	}
	var before string
	// sql-guarantee: a settled receipt must stay byte-identical across recovery; inference requests have no owner read, and the whole stored row is the guarantee.
	if err := a.DB.QueryRow(`SELECT row_to_json(r)::text FROM inference_requests r WHERE id=?`, closedID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	files := []string{"console/chat/pi-home/.pi/agent/models.json", "console/chat/sessions/conversation.jsonl", "workspaces/change-00000000-0001-7000-8000-000000000000/source.go"}
	for _, path := range files {
		full := filepath.Join(a.cfg.dataDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("canary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RecoverInferenceRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	var outcome, usage, first string
	var finished bool
	// sql-guarantee: a settled receipt must stay byte-identical across recovery; inference requests have no owner read, and the whole stored row is the guarantee.
	if err := a.DB.QueryRow(`SELECT outcome,usage_json,finished_at IS NOT NULL,row_to_json(r)::text FROM inference_requests r WHERE id=?`, openID).Scan(&outcome, &usage, &finished, &first); err != nil {
		t.Fatal(err)
	}
	if outcome != "interrupted_restart" || !finished || usage != originalUsage {
		t.Fatalf("interrupted receipt invented completion or usage: %s %s %t", outcome, usage, finished)
	}
	if err := a.RecoverInferenceRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[ids.ID]string{openID: first, closedID: before} {
		var got string
		// sql-guarantee: a settled receipt must stay byte-identical across recovery; inference requests have no owner read, and the whole stored row is the guarantee.
		if err := a.DB.QueryRow(`SELECT row_to_json(r)::text FROM inference_requests r WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("settled receipt %d changed: %v", id, err)
		}
	}
	for _, path := range files {
		body, err := os.ReadFile(filepath.Join(a.cfg.dataDir, path))
		if err != nil || string(body) != "canary" {
			t.Fatalf("recovery changed %s: %v", path, err)
		}
	}
	if _, err := a.DB.Exec(`DROP TABLE inference_requests`); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverInferenceRequests(context.Background()); err == nil {
		t.Fatal("recovery hid a database failure")
	}
}

func TestInferenceUploadCannotReadDaemonCredentials(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	uploads := filepath.Join(a.cfg.dataDir, "console", "chat", "uploads")
	if err := os.MkdirAll(uploads, 0755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(a.cfg.dataDir, "secrets.env")
	if err := os.WriteFile(secret, []byte("PROVIDER_KEY=canary"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(uploads, "screenshot.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/download", nil)
	httpapi.ServeUntrustedFile(a.cfg.dataDir, w, r, link)
	if w.Code != 404 || strings.Contains(w.Body.String(), "canary") {
		t.Fatal("upload symlink exposed daemon credentials")
	}
}
