package access

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// Actor is who an act is recorded against (design/decisions/mergeable-forge-data.md):
// the principal who answers for it, the name as shown when it happened, and
// how they acted. An absent actor is all empty; an anonymous act has no
// principal and Via "anonymous"; a name read without its principal keeps
// the name and no principal. Nothing invents a principal.
type Actor struct {
	Principal ids.ID `json:"principal,omitzero"`
	Name      string `json:"name"`
	Via       string `json:"via,omitempty"`
}

// The ways of acting an actor's Via names. A way with a subject is
// "<way>:<subject>" (token-cli:laptop, chat:<id>, run-step:<step>).
const (
	ViaSession    = "session"
	ViaHeader     = "header"
	ViaOpen       = "open"
	ViaTokenCLI   = "token-cli"
	ViaTokenAgent = "token-agent"
	ViaOAuth      = "oauth"
	ViaChat       = "chat"
	ViaRunStep    = "run-step"
	ViaTicket     = "ticket"
	ViaBinding    = "binding"
	ViaSchedule   = "schedule"
	ViaEval       = "eval"
	ViaWhatIf     = "what-if"
	ViaAnonymous  = "anonymous"
)

// Actor is the principal as an act records it.
func (pr Principal) Actor() Actor {
	a := Actor{Principal: pr.ID, Name: pr.Name}
	switch {
	case pr.Kind == "anonymous":
		a.Via = ViaAnonymous
	case pr.Instrument == "console":
		a.Via = ViaChat + ":" + pr.ConsoleID
	case pr.Instrument == "run":
		a.Via = ViaRunStep
	case pr.Instrument == SystemInstrument && pr.Via == "binding":
		a.Via = ViaBinding + ":" + strings.TrimPrefix(pr.Credential, "system:binding/")
	case pr.Via == "session":
		a.Via = ViaSession
	case pr.Via == "proxy":
		a.Via = ViaHeader
	case pr.Via == "open":
		a.Via = ViaOpen
	case pr.Via == "token" && pr.Purpose == TokenAgent:
		a.Via = ViaTokenAgent + ":" + strings.TrimPrefix(pr.Credential, "token:")
	case pr.Via == "token":
		a.Via = ViaTokenCLI + ":" + strings.TrimPrefix(pr.Credential, "token:")
	case pr.Via == "oauth":
		a.Via = ViaOAuth + ":" + pr.DelegationID.String()
	default:
		a.Via = pr.Via
	}
	return a
}

// Way is the way of acting without its subject: "token-cli" of
// "token-cli:laptop".
func (a Actor) Way() string {
	way, _, _ := strings.Cut(a.Via, ":")
	return way
}

// Assents says whether the act is a person's own assent: acting
// directly — a session, a trusted header, open mode or their command-line
// token. Through a chat, a run step, an agent token, an OAuth client, a
// ticket or an eval, the same person's act is not their assent. It judges
// the way of acting; who the person is, is the principal's to say.
func (a Actor) Assents() bool {
	switch a.Way() {
	case ViaSession, ViaHeader, ViaOpen, ViaTokenCLI:
		return true
	}
	return false
}

// ActorFrom is the actor an act by name is recorded against: the verified
// caller in ctx when it is the one named (its receipt or principal name),
// with its principal and way of acting; otherwise the name alone, with no
// principal, since a name is never taken for a principal.
func ActorFrom(ctx context.Context, name string) Actor {
	if c, ok := CallerFrom(ctx); ok && c.Verified && name != "" && (c.Receipt == name || c.Principal.Name == name) {
		a := c.Principal.Actor()
		a.Name = name
		return a
	}
	return Actor{Name: name}
}

// Person is a principal as it stands now, for showing an actor: its name
// today, and whether its account is disabled or it was removed. Known is
// false for a principal AppTown has no record of.
type Person struct {
	Known    bool
	Name     string
	Disabled bool
	Removed  bool
}

// Shown is how an actor reads in history (design/decisions/mergeable-forge-data.md):
// the person's name today; "(then x)" when they were written as another
// name; "disabled" for a disabled account; "former user (was: x)" for a
// removed one. An act with no principal reads as it was written — the
// AppTown's own names, an instrument — and as "unrecorded actor (written as:
// x)" when it names a person AppTown never resolved.
func (a Actor) Shown(now Person, systemName func(string) bool) string {
	if a.Principal.IsZero() {
		if a.Name == "" || a.Name == "anonymous" || systemName(a.Name) || strings.Contains(a.Name, "/") {
			return a.Name
		}
		return "unrecorded actor (written as: " + a.Name + ")"
	}
	if !now.Known || now.Removed {
		return "former user (was: " + a.Name + ")"
	}
	shown := now.Name
	base, _, _ := strings.Cut(a.Name, " via ")
	if base != now.Name {
		shown += " (then " + base + ")"
	}
	if rest, ok := strings.CutPrefix(a.Name, base); ok && rest != "" {
		shown += rest
	}
	if now.Disabled {
		shown += " (disabled)"
	}
	return shown
}
