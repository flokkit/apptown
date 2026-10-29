package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Flipping the chat slot reaches existing chats: an unpinned session follows
// the slot at attach time; a session whose person named a model keeps it.
func TestUnpinnedChatFollowsTheSlot(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	slots := model.New(db)
	env := Environment{models: model.NewSelection(slots)}
	if err := slots.Set(context.Background(), "chat", ids.Nil, "", "standard", "openrouter/openai/gpt-5.6-luna", "fixture"); err != nil {
		t.Fatal(err)
	}
	born := &console.Session{Meta: console.Meta{Model: "anthropic/claude-opus-5"}}
	if got, err := env.chatModelFor(t.Context(), born); err != nil || got != "openrouter/openai/gpt-5.6-luna" {
		t.Fatalf("an unpinned chat should follow the slot, got %q", got)
	}
	pinned := &console.Session{Meta: console.Meta{Model: "anthropic/claude-opus-5", ModelPinned: true}}
	if got, err := env.chatModelFor(t.Context(), pinned); err != nil || got != "anthropic/claude-opus-5" {
		t.Fatalf("a pinned chat keeps its model, got %q", got)
	}
	// An unread slot is an error, not the model the chat was born with.
	defer db.FailNextStatement("FROM model_slots")()
	if got, err := env.chatModelFor(t.Context(), born); err == nil {
		t.Fatalf("an unread chat slot answered %q", got)
	}
}

// A chat's git remote and pull token are its own project's, named by the id
// it was opened with: after its address moves and another project takes the
// address the chat was opened at, a resumed chat still fetches its own.
func TestAResumedChatFetchesItsOwnProjectAfterItsAddressIsTaken(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ctx := t.Context()
	create := func(slug string) project.Project {
		t.Helper()
		id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		p, err := project.ByIDIn(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	move := func(p project.Project, to string) project.Project {
		t.Helper()
		var after project.Project
		if err := db.InTx(ctx, func(tx *database.Tx) error {
			changed, err := project.UpdateSettingsIn(ctx, tx, p.ID, p.Slug, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Slug: &to})
			after = changed.After
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return after
	}
	a := move(create("first"), "ledger")
	chat := &console.Session{Meta: console.Meta{Labels: LabelsFor(&a, nil)}}
	a = move(a, "later")
	b := create("ledger")
	people := identity.New(db, identity.Config{})
	env := Environment{projects: project.New(db, nil), identity: people, cfg: EnvironmentConfig{APIURL: "http://apptown.test"}}
	remote, err := env.chatRemote(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(remote, "\n")
	if !strings.Contains(joined, "APPTOWN_MAIN_REMOTE=http://apptown.test/git/later") {
		t.Fatalf("remote %q; want project %s's current address", joined, a.ID)
	}
	_, token, _ := strings.Cut(joined, "Authorization: Bearer ")
	owner, found, err := people.ProjectOfPullToken(ctx, strings.TrimSpace(token))
	if err != nil || !found || owner != a.ID || owner == b.ID {
		t.Fatalf("the chat's pull token is project %s's (%v %v); want %s", owner, found, err, a.ID)
	}
}
