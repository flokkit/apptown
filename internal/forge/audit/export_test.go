package audit_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// Ledger selection is safe by default: a line moves only when its kind is
// about the project itself and it names no work, or when it names work and
// all of it moves, read the way current writers record it (a change's
// subject, prefixed, bare or with its run; a run's subject with its
// successor; a chat's session as subject or in the actor; a change or run
// in the payload or its resource). Every other line stays, counted by kind,
// moving lines byte for byte.
func TestLedgerLinesMoveOnlyWhenAboutTheProjectOrMovingWork(t *testing.T) {
	// Lines carry the refs their writer derives; these are lines from before
	// the cutover, whose integers and chat names resolve through former ids.
	chats := map[string]ids.ID{"abc123abc123": idstest.N(1000), "def456def456": idstest.N(1001)}
	former := func(kind, token string) (ids.ID, bool) {
		if kind == audit.RefChat {
			id, ok := chats[token]
			return id, ok
		}
		n, err := strconv.ParseUint(token, 10, 64)
		return idstest.N(n), err == nil && n > 0
	}
	line := func(kind, actor, subject, payload string) json.RawMessage {
		refs := audit.Refs(kind, actor, subject, json.RawMessage(payload), former)
		b, _ := json.Marshal(map[string]any{"kind": kind, "actor": actor, "subject": subject, "payload_json": json.RawMessage(payload), "refs": refs})
		return b
	}
	stays := []json.RawMessage{
		line("change.abandoned", "bls", "change 7", `{"reason":"x"}`),
		line("change.attachment-added", "bls", "change 7: brief.txt", `{}`),
		line("change.created", "bls", "Try blue", `{"changeId":7,"state":"in_progress"}`),
		line("change.preview", "bls", "7", `{"url":"http://private-skipped-preview"}`),
		line("change.boot-check", "forge", "7", `{"ok":false,"error":"diagnostic"}`),
		line("wow.uplift.opened", "bls", "7", `{}`),
		line("integration.wire", "bls", "errors", `{"change":7,"mode":"wire"}`),
		line("authz.denied", "bls", "change:land", `{"resource":"change:7"}`),
		line("authz.denied", "bls", "run:advance", `{"resource":"run:11"}`),
		line("change.run-started", "bls", "change 8 run 11 (verify)", `null`),
		line("console.invoke", "boss via console/abc123abc123", "run-12", `{}`),
		line("landing.restarted", "bls", "run 3 -> 11", `null`),
		line("release.deployed", "apptown-system", "main", `{"runId":11}`),
		line("remediation.item.stuck", "apptown-system", "1", `{"change":7,"reason":"private-skipped-attempt-diagnostic"}`),
		line("remediation.item.started", "apptown-system", "4", `{"change":8,"run":11}`),
		line("console.say", "bls", "abc123abc123", `{"opener":"bls"}`),
		line("change.saved", "chat/abc123abc123", "change 8", `{}`),
		line("change.started", "bls", "change 99", `null`),       // a change not known to move
		line("deployment.backup", "bls", "main", `{"backup":7}`), // not shown to be the project's own
		line("workitem.created", "bls", "Old name", `{}`),        // an older kind no rule reads
		line("project.created", "bls", "app", `{"changeId":7}`),  // a project kind naming work that stays
	}
	moves := []json.RawMessage{
		line("adoption.recorded", "bls", "r2", `{"digest":"d"}`),
		line("project.extension.enabled", "bls", "7", `null`),
		line("secret.bound", "bls", "API_KEY", `{"deployment":"main"}`),
		line("change.started", "bls", "change 8", `null`),
		line("change.preview", "bls", "8", `{}`),
		line("change.created", "bls", "Make it faster", `{"changeId":8}`),
		line("change.run-started", "bls", "change 8 run 12 (verify)", `null`),
		line("console.say", "bls", "def456def456", `{}`),
		line("console.invoke", "boss via console/def456def456", "run-12", `{}`),
		line("remediation.item.stuck", "apptown-system", "2", `{"change":8}`),
		line("landing.restarted", "bls", "run 12 -> 13", `null`),
		line("authz.denied", "bls", "change:land", `{"resource":"change:8"}`),
	}
	got, err := audit.Select(append(append([]json.RawMessage{}, stays...), moves...), audit.Moving{Changes: []ids.ID{idstest.N(8)}, Runs: []ids.ID{idstest.N(12), idstest.N(13)}, Chats: []ids.ID{idstest.N(1001)}})
	if err != nil {
		t.Fatal(err)
	}
	stayed := 0
	for _, n := range got.Stayed {
		stayed += n
	}
	if stayed != len(stays) || len(got.Lines) != len(moves) || got.Stayed["workitem.created"] != 1 || got.Stayed["remediation.item.stuck"] != 1 {
		t.Fatalf("%d stayed (%v), %d moved: %s", stayed, got.Stayed, len(got.Lines), got.Lines)
	}
	for i := range moves {
		if string(got.Lines[i]) != string(moves[i]) {
			t.Fatalf("line %d changed or the wrong one moved: %s", i, got.Lines[i])
		}
	}
}
