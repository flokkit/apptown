package console_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// follower is a page following a chat: it folds each update into the
// messages it holds, as the page does, and keeps the draft and cursor.
type follower struct {
	updates chan console.Update
	held    []console.Message
	draft   *console.Message
	cursor  console.Cursor
	stop    context.CancelFunc
	done    chan error
}

func follow(t *testing.T, chats *console.Service, id string, after console.Cursor) *follower {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &follower{updates: make(chan console.Update, 100), stop: cancel, done: make(chan error, 1)}
	go func() {
		f.done <- chats.Follow(ctx, id, after, func(u console.Update) error {
			f.updates <- u
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-f.done })
	return f
}

// next folds the next update, waiting at most a few seconds for it.
func (f *follower) next(t *testing.T) console.Update {
	t.Helper()
	select {
	case u := <-f.updates:
		if u.From > len(f.held) {
			t.Fatalf("an update from %d to a follower holding %d messages: a gap", u.From, len(f.held))
		}
		f.held = append(f.held[:u.From:u.From], u.Messages...)
		f.draft, f.cursor = u.Draft, u.Cursor
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("no update")
	}
	return console.Update{}
}

// until folds updates until cond holds of what the follower has.
func (f *follower) until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case u := <-f.updates:
			f.updates <- u
			f.next(t)
		case <-deadline:
			t.Fatalf("never: %s (holding %d messages, draft %+v)", what, len(f.held), f.draft)
		}
	}
}

// matches says the follower holds exactly what a fresh read of the chat
// answers: nothing missing, nothing twice.
func (f *follower) matches(t *testing.T, chats *console.Service, id string) {
	t.Helper()
	page, err := chats.Messages(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.held, page.Messages) {
		t.Fatalf("the follower holds\n%+v\na fresh read has\n%+v", f.held, page.Messages)
	}
}

func openChat(t *testing.T) (*console.Service, *consoletest.Agent, *console.Session) {
	t.Helper()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(context.Background(), console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := chats.Send(context.Background(), s.ID, "make it blue"); err != nil {
		t.Fatal(err)
	}
	return chats, agent, s
}

// The helper's words reach a follower as pi writes them, before the record
// holds the message; once it does, the same update shows the message and
// no draft, so the words are never missing and never shown twice.
func TestWordsReachAFollowerAsPiWritesThem(t *testing.T) {
	chats, agent, s := openChat(t)
	f := follow(t, chats, s.ID, "")
	f.until(t, "the person's message", func() bool { return len(f.held) == 1 })

	sent := consoletest.PiNow()
	for _, words := range []string{"Let", "Let me look", "Let me look first."} {
		agent.Drafting(s.ID, words, sent)
		f.until(t, "the draft "+words, func() bool { return f.draft != nil && f.draft.Text == words })
		if len(f.held) != 1 || f.draft.Role != "helper" || f.draft.I != 1 {
			t.Fatalf("a draft is the helper's next bubble: %+v after %d messages", f.draft, len(f.held))
		}
	}

	agent.ReplySent(s.ID, "Let me look first.", sent)
	u := f.next(t)
	if u.Draft != nil || len(f.held) != 2 || f.held[1].Text != "Let me look first." {
		t.Fatalf("the recorded message replaces its draft in one update: %+v, holding %+v", u, f.held)
	}
	f.matches(t, chats, s.ID)
}

// A draft is shown only while it is the chat's own and newer than
// everything recorded: an older one (pi stopped mid-message, then something
// was recorded) and one naming another record file (a delegate's) are not.
func TestOnlyTheChatsOwnNewestDraftShows(t *testing.T) {
	chats, agent, s := openChat(t)
	agent.Drafting(s.ID, "half a thought", time.Now().Add(-time.Hour))
	f := follow(t, chats, s.ID, "")
	if u := f.next(t); u.Draft != nil {
		t.Fatalf("a draft older than the record shows: %+v", u.Draft)
	}
	later := time.Now().Add(time.Hour)
	agent.DraftingFor(s.ID, "2026-01-01T00-00-00-000Z_delegate.jsonl", "a delegate's words", later)
	select { // three reads of the chat at least
	case u := <-f.updates:
		t.Fatalf("an update for a draft that is not the chat's: %+v", u)
	case <-time.After(300 * time.Millisecond):
	}
	agent.Drafting(s.ID, "my own words", later)
	f.until(t, "the chat's own draft", func() bool { return f.draft != nil && f.draft.Text == "my own words" })
}

// A follower that reconnects with its cursor gets only what it does not
// hold; a cursor whose last message no longer matches gets everything
// again, which the follower takes in place of what it held.
func TestAFollowerResumesFromItsCursor(t *testing.T) {
	chats, agent, s := openChat(t)
	agent.ReplySent(s.ID, "done", consoletest.PiNow())
	first := follow(t, chats, s.ID, "")
	first.until(t, "two messages", func() bool { return len(first.held) == 2 })
	first.stop()

	if err := chats.Send(context.Background(), s.ID, "thanks"); err != nil {
		t.Fatal(err)
	}
	again := follow(t, chats, s.ID, first.cursor)
	again.held = first.held
	if u := again.next(t); u.From != 2 || len(u.Messages) != 1 || u.Messages[0].Text != "thanks" {
		t.Fatalf("a resumed follower is sent only what it lacks: from %d, %d messages", u.From, len(u.Messages))
	}
	again.matches(t, chats, s.ID)

	stale := follow(t, chats, s.ID, console.Cursor(strings.SplitN(string(first.cursor), ".", 2)[0]+".000000000000"))
	stale.held = first.held
	if u := stale.next(t); u.From != 0 || len(u.Messages) != 3 {
		t.Fatalf("a cursor that does not match is sent everything: from %d, %d messages", u.From, len(u.Messages))
	}
	stale.matches(t, chats, s.ID)
}

// The projection changes other than by appending, and a follower ends up
// holding what a fresh read answers after each: a tool marked done when
// its result is recorded, and the record starting over in a new file. A
// failed model call shows only once pi settles with it.
func TestAFollowerKeepsUpWithChangesBehindTheTail(t *testing.T) {
	chats, agent, s := openChat(t)
	agent.ReplySent(s.ID, "checking", consoletest.PiNow(), "c7")
	f := follow(t, chats, s.ID, "")
	f.until(t, "the tool call", func() bool { return len(f.held) == 2 })
	if f.held[1].Tools[0].Done {
		t.Fatal("a tool is done before its result is recorded")
	}
	agent.ToolResult(s.ID, "c7")
	f.until(t, "the tool done", func() bool { return len(f.held) == 2 && f.held[1].Tools[0].Done })
	f.matches(t, chats, s.ID)

	// A failed model call is not shown while pi may retry it, nor while it
	// waits to; the failure it settles with is, at once, and ends the turn.
	agent.FailedTurn(s.ID)
	agent.Retry(s.ID)
	agent.FailedTurn(s.ID)
	select { // three reads of the chat at least
	case u := <-f.updates:
		if len(u.Messages) > 0 {
			t.Fatalf("a failure pi has not settled with is shown: %+v", u)
		}
	case <-time.After(300 * time.Millisecond):
	}
	agent.Settled(s.ID)
	f.until(t, "the failed turn's bubble", func() bool { return len(f.held) == 3 && f.held[2].Error })
	f.matches(t, chats, s.ID)

	agent.NewRecordFile(s.ID)
	f.until(t, "the new record's empty conversation", func() bool { return len(f.held) == 0 })
	f.matches(t, chats, s.ID)
}

// Following stops when the chat goes, and its error says so.
func TestFollowingEndsWithTheChat(t *testing.T) {
	chats, _, s := openChat(t)
	f := follow(t, chats, s.ID, "")
	f.next(t)
	if _, err := chats.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.done:
		f.done <- err
		if !errors.Is(err, console.ErrNoSession) {
			t.Fatalf("following an ended chat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following outlived the chat")
	}
}

// A record that cannot be read is never followed as a conversation with no
// words, nor as a turn that is over: following it fails, sending nothing.
func TestARecordThatCannotBeReadIsNotFollowedAsEmptyOrDone(t *testing.T) {
	t.Run("from the first reading", func(t *testing.T) {
		chats, _, s := openChat(t)
		sessions := filepath.Join(s.Dir, "sessions")
		if err := os.RemoveAll(sessions); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sessions, []byte("not a folder"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := chats.Follow(context.Background(), s.ID, "", func(u console.Update) error {
			t.Fatalf("an unreadable record was sent as %+v", u)
			return nil
		})
		if err == nil {
			t.Fatal("following an unreadable record did not fail")
		}
	})
	t.Run("after it was read", func(t *testing.T) {
		chats, _, s := openChat(t)
		f := follow(t, chats, s.ID, "")
		f.until(t, "the person's message", func() bool { return len(f.held) == 1 })
		// The record's folder goes unreadable.
		sessions := filepath.Join(s.Dir, "sessions")
		if err := os.Rename(sessions, sessions+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sessions, []byte("not a folder"), 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-f.done:
			f.done <- err
			if err == nil {
				t.Fatal("following ended without saying the record could not be read")
			}
		case u := <-f.updates:
			t.Fatalf("an unreadable record was sent as %+v", u)
		case <-time.After(5 * time.Second):
			t.Fatal("following went on over an unreadable record")
		}
	})
}
