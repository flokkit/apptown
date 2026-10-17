package console_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
)

// A scope's transcript says which of its chats have ended and hands back
// each chat's labels unread, before and after a restart: a past-chats list
// needs both, and the record on disk is the only source of either.
func TestTranscriptSaysWhichChatsEndedAndKeepsTheirLabels(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
	open := func(text string, labels map[string]string) *console.Session {
		s, err := chats.Open(ctx, console.OpenRequest{Scope: "project:7", Labels: labels, Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls"})
		if err != nil {
			t.Fatal(err)
		}
		if text != "" {
			if err := chats.Send(ctx, s.ID, text); err != nil {
				t.Fatal(err)
			}
			agent.Reply(s.ID, "ok")
		}
		return s
	}
	live := open("still talking", map[string]string{"project": "7"})
	ended := open("all done", map[string]string{"project": "7", "kind": "kept"})
	empty := open("", map[string]string{"project": "7"})
	for _, s := range []*console.Session{ended, empty} {
		if _, err := chats.End(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	check := func(svc *console.Service, when string) {
		convs, unreadable, err := svc.Transcript(ctx, "project:7")
		if err != nil || len(unreadable) != 0 {
			t.Fatal(err, unreadable)
		}
		got := map[string]console.Conversation{}
		for _, c := range convs {
			got[c.ID] = c
		}
		if len(got) != 2 {
			t.Fatalf("%s: an empty chat is not in the transcript: %+v", when, convs)
		}
		if c := got[live.ID]; c.Ended {
			t.Fatalf("%s: a live chat read as ended", when)
		}
		if c := got[ended.ID]; !c.Ended || c.Labels["kind"] != "kept" {
			t.Fatalf("%s: the ended chat: %+v", when, c)
		}
	}
	check(chats, "running")
	again := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
	check(again, "before resume")
	if err := again.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	check(again, "after restart")
}

// One chat folder whose record cannot be read does not hide the others: the
// scope's readable chats are returned, and the folder is named as
// unreadable, since it may belong to the scope.
func TestAnUnreadableChatRecordIsNamedNotAnOutage(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
	open := func(scope string) *console.Session {
		s, err := chats.Open(ctx, console.OpenRequest{Scope: scope, Labels: map[string]string{"project": "7"}, Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls"})
		if err != nil {
			t.Fatal(err)
		}
		if err := chats.Send(ctx, s.ID, "hello"); err != nil {
			t.Fatal(err)
		}
		agent.Reply(s.ID, "ok")
		if _, err := chats.End(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		return s
	}
	good, bad := open("project:7"), open("project:8")
	if err := os.WriteFile(filepath.Join(bad.Dir, console.EndedFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	convs, unreadable, err := chats.Transcript(ctx, "project:7")
	if err != nil || len(convs) != 1 || convs[0].ID != good.ID || len(unreadable) != 1 || unreadable[0] != bad.ID {
		t.Fatalf("transcript: %+v unreadable %v err %v", convs, unreadable, err)
	}
	records, unreadable, err := chats.RecordsMatching(console.Match{Labels: map[string]string{"project": "7"}})
	if err != nil || len(records) != 1 || len(unreadable) != 1 || unreadable[0] != bad.ID {
		t.Fatalf("records: %+v unreadable %v err %v", records, unreadable, err)
	}
	if err := chats.RemoveEnded(console.Match{Labels: map[string]string{"project": "7"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bad.Dir); err != nil {
		t.Fatalf("a folder whose record cannot be read was deleted on a guess: %v", err)
	}
}
