package audit

import (
	"encoding/json"
	"regexp"

	"flokkit.io/forge/internal/ids"
)

// The work a ledger line is about, kept typed beside its words
// (design/decisions/mergeable-forge-data.md): which changes,
// runs and chats it names. The writer derives them once, from the line's
// subject, payload and actor, so nothing reads them back out of the text;
// the identity cutover derived them for the lines written before.

// Reference kinds.
const (
	RefChange = "change"
	RefRun    = "run"
	RefChat   = "chat"
)

// Ref is one piece of work a line names: its kind and identity, or, when the
// text named something that could not be resolved, the text itself. An
// unresolved reference never counts as moving work.
type Ref struct {
	Kind       string `json:"kind"`
	ID         ids.ID `json:"id"`
	Unresolved string `json:"unresolved,omitempty"`
}

// Resolver turns a reference token read from a line (a UUID; before the
// cutover, an integer or a 12-hex chat id) into a record identity.
type Resolver func(kind, token string) (ids.ID, bool)

// ParseIDs resolves a token that is a record id: what lines written since
// the cutover carry.
func ParseIDs(_ string, token string) (ids.ID, bool) {
	id, err := ids.Parse(token)
	return id, err == nil && !id.IsZero()
}

// subjectForm is how a receipt kind's subject names the work it is about.
type subjectForm int

const (
	subjectNone     subjectForm = iota
	subjectChange               // "change N", "change N: ...", "change N run M ..."
	subjectChangeID             // "N", a change's id
	subjectRun                  // "run N", "run-N", "run N -> M"
	subjectChat                 // a chat's session id
)

// subjectForms are the kinds whose subject names a change, run or chat.
// Every line is also read for a change or run in its payload ("changeId",
// "runId", "change", "run", or a "resource" of "change:N" / "run:N") and a
// chat in its actor ("chat/<s>", "... via console/<s>").
var subjectForms = map[string]subjectForm{
	"change.abandoned": subjectChange, "change.attachment-added": subjectChange, "change.attachment-removed": subjectChange,
	"change.edited": subjectChange, "change.imported": subjectChange, "change.landing": subjectChange,
	"change.narrative-updated": subjectChange, "change.revision-pushed": subjectChange, "change.revision-refused": subjectChange,
	"change.revision-synced": subjectChange, "change.run-started": subjectChange, "change.save-refused": subjectChange,
	"change.saved": subjectChange, "change.sendback": subjectChange, "change.services-edit": subjectChange,
	"change.started": subjectChange, "change.ticket-already-spent": subjectChange, "change.ticket-minted": subjectChange,
	"change.titled": subjectChange, "chat.save-discarded": subjectChange,
	"change.boot-check": subjectChangeID, "change.preview": subjectChangeID, "wow.uplift.opened": subjectChangeID,
	"console.invoke": subjectRun, "run.abandoned": subjectRun,
	"landing.merge-unknown": subjectRun, "landing.paused-by-law": subjectRun, "landing.restarted": subjectRun,
	"console.start": subjectChat, "console.model-followed": subjectChat, "console.say": subjectChat,
	"console.interrupt": subjectChat, "console.upload": subjectChat, "console.terminal": subjectChat, "console.end": subjectChat,
}

// A token is a UUID, or an integer or 12-hex chat id from before the cutover.
const refToken = `([0-9a-fA-F][0-9a-fA-F-]*)`

var (
	changeSubject = regexp.MustCompile(`^change ` + refToken + `\b(?:.*?\brun ` + refToken + `)?`)
	numberSubject = regexp.MustCompile(`^` + refToken + `$`)
	runSubject    = regexp.MustCompile(`^run[ /-]` + refToken + `(?: -> ` + refToken + `)?`)
	consoleActor  = regexp.MustCompile(`(?:^chat/| via console/)` + refToken)
	resourceRef   = regexp.MustCompile(`^(change|run):` + refToken + `$`)
)

// Refs reads the work a line names by the rules above, each token resolved
// by resolve. A token that does not resolve is kept as Unresolved.
func Refs(kind, actor, subject string, payloadRaw json.RawMessage, resolve Resolver) []Ref {
	var out []Ref
	seen := map[Ref]bool{}
	add := func(kind, tok string) {
		if tok == "" {
			return
		}
		r := Ref{Kind: kind}
		if id, ok := resolve(kind, tok); ok {
			r.ID = id
		} else {
			r.Unresolved = tok
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	var payload map[string]any
	_ = json.Unmarshal(payloadRaw, &payload) // a payload that is not an object names nothing
	for _, k := range []struct{ key, kind string }{{"changeId", RefChange}, {"change", RefChange}, {"runId", RefRun}, {"run", RefRun}} {
		switch v := payload[k.key].(type) {
		case string:
			if _, err := ids.Parse(v); err == nil {
				add(k.kind, v)
			}
		case float64:
			if v > 0 {
				add(k.kind, jsonNumber(v))
			}
		}
	}
	if r, ok := payload["resource"].(string); ok {
		if m := resourceRef.FindStringSubmatch(r); m != nil {
			add(m[1], m[2])
		}
	}
	if m := consoleActor.FindStringSubmatch(actor); m != nil {
		add(RefChat, m[1])
	}
	switch subjectForms[kind] {
	case subjectChange:
		if m := changeSubject.FindStringSubmatch(subject); m != nil {
			add(RefChange, m[1])
			add(RefRun, m[2])
		}
	case subjectChangeID:
		if m := numberSubject.FindStringSubmatch(subject); m != nil {
			add(RefChange, m[1])
		}
	case subjectRun:
		if m := runSubject.FindStringSubmatch(subject); m != nil {
			add(RefRun, m[1])
			add(RefRun, m[2])
		}
	case subjectChat:
		add(RefChat, subject)
	}
	return out
}

// jsonNumber spells a JSON number as the integer it holds.
func jsonNumber(f float64) string {
	b, _ := json.Marshal(int64(f))
	return string(b)
}
