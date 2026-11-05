package apptown

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The rehearse loop and the five-minute round are stoppable: ending a
// session returns its loop at once, and StopConsoles returns every loop and
// the round without touching the sessions themselves.
func TestConsoleLoopsStop(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{ConsoleAgent: consoletest.NewAgent()})
	s := a.Console.Restore(consoleMetaFor(a.Console.NewSessionID(), idstest.N(1), idstest.N(5)))
	finished := make(chan struct{})
	go func() {
		a.changeConsole.Rehearse(context.Background(), s)
		close(finished)
	}()
	if _, err := a.Console.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("rehearseLoop must return when the session ends")
	}

	roundDone := make(chan struct{})
	go func() {
		a.consoleRounds(context.Background(), a.consoleStop)
		close(roundDone)
	}()
	s2 := a.Console.Restore(consoleMetaFor(a.Console.NewSessionID(), idstest.N(1), idstest.N(6)))
	loop2 := make(chan struct{})
	go func() {
		a.changeConsole.Rehearse(context.Background(), s2)
		close(loop2)
	}()
	a.stopConsoles(context.Background())
	for name, ch := range map[string]chan struct{}{"round": roundDone, "rehearse loop": loop2} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s must return on StopConsoles", name)
		}
	}
	// Sessions survive a stop: the registry is untouched for the next recovery.
	if _, err := a.Console.Get(context.Background(), s2.ID); err != nil {
		t.Error("StopConsoles must not remove sessions")
	}
}

// An idle session is stopped by the round; an ended one is not touched.
func TestConsoleIdleStopSkipsEndedSessions(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := testApp(t, Config{ConsoleAgent: agent})
	m := consoleMetaFor("idle1", idstest.N(1), ids.Nil)
	m.Checkout = t.TempDir()
	m.LastUsed = time.Now().Add(-time.Hour)
	s := a.Console.Restore(m)
	if err := agent.Start(context.Background(), console.Launch{ID: s.ID, Dir: s.Dir}); err != nil {
		t.Fatal(err)
	}

	a.consoleIdleStop(context.Background())
	if agent.Running(s.ID) {
		t.Fatal("the idle round did not stop the agent")
	}
	if _, err := a.Console.Get(context.Background(), s.ID); err != nil {
		t.Fatal("the idle round forgot the session")
	}
}

type previewChatHome struct{}

func (previewChatHome) ConsoleFiles(context.Context, string, ids.ID) (map[string]console.Staged, error) {
	return nil, nil
}

type previewChatCLI struct{}

func (previewChatCLI) Path() (string, error) { return "", errors.New("no CLI in this fixture") }

type previewChatCredentials struct{}

func (previewChatCredentials) CheckEnvironment(context.Context, []string) error { return nil }

// Prepare is called for every wake, for local and worker chats alike. A
// continuation follows that same path, and project/read-only chats receive none.
func TestChatPreparationRecordsOnlyActualContainerDelivery(t *testing.T) {
	t.Parallel()
	db := testApp(t).DB
	ctx := t.Context()
	id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	people := identity.New(db, identity.Config{OpenInstance: true})
	if err := people.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	ctx = access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	projects := project.New(db, people)
	secrets := secret.NewService(db, secret.Config{Projects: secretProjectScope{project.New(db, nil)}, Authority: access.Checked{Authority: people, Projects: projects}})
	if _, err := secrets.BindStored(ctx, id, "demo", "preview", "NEXTUP_PASSWORD", "short", "boss"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	if err := projects.SetDefaultBranch(ctx, id, "main"); err != nil {
		t.Fatal(err)
	}
	ch, err := workflow.NewCreator(db, func(context.Context, workflow.CreatedChange) error { return nil }).Create(ctx, workflow.ChangeDraft{ProjectID: id, Title: "Check preview"}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		_, err := scm.AdvanceAuthorityIn(ctx, tx, id, ch.Branch, scm.ChatHolder("chat-one"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := project.ByIDIn(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, secret.SpecFile), []byte("[project]\nname = \"demo\"\nrevision = \"1.0\"\n[profiles.preview]\nNEXTUP_PASSWORD = { required = true }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bindings := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	e := conversation.NewEnvironment(db, conversation.EnvironmentDependencies{Projects: projects, Secrets: chatAgentEnvironment{secrets}, Bindings: bindings.Bindings, Instructions: agentenv.NewInstructions(db, bindings), Home: previewChatHome{}, CLI: previewChatCLI{}, Credentials: previewChatCredentials{}, Identity: people}, conversation.EnvironmentConfig{DataDir: t.TempDir(), APIURL: "http://apptown.test", InferenceEnabled: true})
	chat := &console.Session{Meta: console.Meta{ID: "chat-one", Actor: "boss", Labels: conversation.LabelsFor(&p, ch), ModelPinned: true, Checkout: ws, Writable: true, ResumedFrom: "agent step"}, Dir: t.TempDir()}
	for _, placement := range []string{"", conversation.OnWorker} {
		chat.Labels[conversation.PlacementLabel] = placement
		for range 2 {
			given, err := e.Prepare(ctx, chat)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(given.Env, "NEXTUP_PASSWORD=short") {
				t.Fatal("chat wake missing preview value")
			}
		}
	}
	rows, err := audit.Recent(ctx, db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || len(rows) != 0 {
		t.Fatal("preparation recorded a delivery", err)
	}
	given, err := e.Prepare(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	if err := given.Creating(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err = audit.Recent(ctx, db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || len(rows) != 1 || !strings.Contains(string(rows[0].Payload), given.SecretDigest) || !strings.Contains(strings.ReplaceAll(string(rows[0].Payload), " ", ""), "\"state\":\"pending\"") {
		t.Fatal("missing expected delivery before container creation", err)
	}
	if err := given.Created(ctx, "apptown-console-chat-one:instance-one"); err != nil {
		t.Fatal(err)
	}
	rows, err = audit.Recent(ctx, db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || len(rows) != 2 || !strings.Contains(string(rows[0].Payload), "apptown-console-chat-one:instance-one") || !strings.Contains(strings.ReplaceAll(string(rows[0].Payload), " ", ""), "\"state\":\"delivered\"") {
		t.Fatal("missing actual container evidence", err)
	}
	if err := given.Created(ctx, ""); err != nil {
		t.Fatal(err)
	}
	rows, err = audit.Recent(ctx, db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || !strings.Contains(strings.ReplaceAll(string(rows[0].Payload), " ", ""), "\"state\":\"not-delivered\"") {
		t.Fatal("missing failed creation outcome", err)
	}
	for _, record := range []func() error{
		func() error { return given.Creating(ctx) },
		func() error { return given.Created(ctx, "container-two") },
	} {
		restore := dbtest.NextStatementFailure(db, "-- name: AppendReceipt")
		err = record()
		restore()
		if err == nil {
			t.Fatal("delivery evidence failure ignored")
		}
	}
	chat.Writable = false
	given, err = e.Prepare(ctx, chat)
	if err != nil || slices.Contains(given.Env, "NEXTUP_PASSWORD=short") {
		t.Fatal("read-only chat received value", err)
	}
	chat.Labels = conversation.LabelsFor(&p, nil)
	chat.Writable = true
	given, err = e.Prepare(ctx, chat)
	if err != nil || slices.Contains(given.Env, "NEXTUP_PASSWORD=short") {
		t.Fatal("project chat received value", err)
	}
	if err := secrets.SetAgentVisible(ctx, id, "preview", "NEXTUP_PASSWORD", false); err != nil {
		t.Fatal(err)
	}
	chat.Labels = conversation.LabelsFor(&p, ch)
	given, err = e.Prepare(ctx, chat)
	if err != nil || slices.Contains(given.Env, "NEXTUP_PASSWORD=short") {
		t.Fatal("wake ignored toggle off", err)
	}
}
