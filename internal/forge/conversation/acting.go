package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
)

// Who may look at a chat and who may act on it (design/subsystems/console.md,
// "Looking and acting").
//
// A chat is not private to the person who opened it. Anyone who can read its
// project may look at it: its conversation, its terminal's screen and who
// opened it. Acting on it needs change:invoke-lane on its project, the
// authority that opens a chat: saying something, pasting a picture, stopping
// its turn, typing into its terminal, ending it (which saves its work), and
// waking it. A chat's own agent may act on its own chat and no other; no
// other instrument acts on a chat.
//
// The chat keeps running with its opener's authority; the record of each act
// names the person who did it, with the chat's opener beside them.
type Chats struct {
	Sessions  *console.Service
	Authority access.Authorizer
	DB        *database.DB
	// Now is the forge's clock, which stamps a chat's acts (the real clock
	// when nil).
	Now func() time.Time
}

var ErrNotThisChat = fault.New(fault.Forbidden, "a chat may only act on its own session")

// Act answers who is acting on the chat, or why the caller may not; what
// names the act in the refusal ("saying something to"). The decision is
// kept as a receipt; each operation records its own act.
func (c Chats) Act(ctx context.Context, s *console.Session, what string) (string, error) {
	return c.decide(ctx, s, what, true)
}

// MayAct is Act as a question, for the pages and for warming: it offers
// acting only to whoever would be allowed it, and keeps no receipt. It is
// presentation, never the boundary.
func (c Chats) MayAct(ctx context.Context, s *console.Session) bool {
	_, err := c.decide(ctx, s, "acting on", false)
	return err == nil
}

func (c Chats) decide(ctx context.Context, s *console.Session, what string, consequential bool) (string, error) {
	caller, _ := access.CallerFrom(ctx)
	if pr := caller.Principal; pr.Kind == "instrument" && (pr.Instrument != "console" || pr.ConsoleID != s.ID) {
		return "", ErrNotThisChat
	}
	if c.Authority == nil {
		return "", access.ErrAuthorityUnavailable
	}
	scope := Of(s)
	got, err := c.Authority.Require(ctx, access.Permission{Capability: access.CapChangeInvokeLane,
		Resource: access.Resource{Type: "project", ID: scope.ProjectID.String(), ProjectID: scope.ProjectID, ChangeID: scope.ChangeID},
		Context:  access.Context{Route: "chat", Consequential: consequential}})
	if err != nil {
		return "", viewingOnly(err, what)
	}
	return got.Receipt, nil
}

// viewingOnly says a person who holds no authority to act here that they
// are viewing, in the chat's words; a disabled account, a credential's
// narrower consent or anything else keeps its own reason.
func viewingOnly(err error, what string) error {
	var denied *access.Denied
	if !errors.As(err, &denied) || (denied.Code != access.DenyMissingCapability && denied.Code != access.DenyOutsideScope) {
		return err
	}
	role := "Project contributor"
	if r := access.RoleByName(access.RoleProjectContrib); r != nil {
		role = r.Title
	}
	return &access.Denied{Code: denied.Code, Reason: fmt.Sprintf("Viewing only: %s this chat needs the %s role.", what, role)}
}

// Record keeps an act on the chat under the person who did it, naming the
// chat's opener too: "bob ended alice's chat".
func (c Chats) Record(ctx context.Context, actor string, s *console.Session, kind string, detail map[string]any) {
	out := map[string]any{"opener": s.Actor}
	for k, v := range detail {
		out[k] = v
	}
	audit.At(clock.Or(c.Now)()).BestEffort(ctx, c.DB, actor, Of(s).ProjectID, kind, s.ID, out)
}

// Say types words into the chat.
func (c Chats) Say(ctx context.Context, s *console.Session, text string) (string, error) {
	actor, err := c.Act(ctx, s, "saying something to")
	if err != nil {
		return "", err
	}
	if err := c.Sessions.Send(ctx, s.ID, text); err != nil {
		return actor, err
	}
	c.Record(ctx, actor, s, "console.say", nil)
	return actor, nil
}

// Interrupt stops the chat's turn.
func (c Chats) Interrupt(ctx context.Context, s *console.Session) error {
	actor, err := c.Act(ctx, s, "stopping")
	if err != nil {
		return err
	}
	if err := c.Sessions.Interrupt(ctx, s.ID); err != nil {
		return err
	}
	c.Record(ctx, actor, s, "console.interrupt", nil)
	return nil
}

// Upload pastes a picture into the chat.
func (c Chats) Upload(ctx context.Context, s *console.Session, f console.File) (console.Attachment, error) {
	actor, err := c.Act(ctx, s, "pasting a picture into")
	if err != nil {
		return console.Attachment{}, err
	}
	att, err := c.Sessions.Upload(ctx, s.ID, f)
	if err != nil {
		return att, err
	}
	c.Record(ctx, actor, s, "console.upload", map[string]any{"name": att.Name})
	return att, nil
}

// Messages is the conversation from bubble `from` on. Looking needs only
// the project's read and never wakes the chat, whoever looks: a chat wakes
// when someone acts on it (says something, opens its terminal to type),
// and those acts are recorded under them.
func (c Chats) Messages(ctx context.Context, s *console.Session, from int) (console.MessagePage, error) {
	return c.Sessions.Messages(ctx, s.ID, from)
}

// Terminal opens the chat's terminal. Someone who may act on the chat gets
// it to type into, waking the chat first; anyone else watches it: its
// screen only, while it runs (console.Service.Watch). The typist's
// authority is asked again before each keystroke or resize it sends, so a
// terminal opened by someone who has since lost it only shows.
func (c Chats) Terminal(ctx context.Context, s *console.Session) (console.Terminal, error) {
	if !c.MayAct(ctx, s) {
		return c.Sessions.Watch(ctx, s.ID)
	}
	actor, err := c.Act(ctx, s, "typing into")
	if err != nil {
		return nil, err
	}
	term, err := c.Sessions.Attach(ctx, s.ID, console.Size{})
	if err != nil {
		return nil, err
	}
	c.Record(ctx, actor, s, "console.terminal", nil)
	return typist{Terminal: term, may: func() bool { return c.MayAct(ctx, s) }}, nil
}

// typist is a terminal whose input goes through only while the person who
// opened it may still act on the chat; otherwise it is a watcher's.
type typist struct {
	console.Terminal
	may func() bool
}

func (t typist) Write(p []byte) (int, error) {
	if !t.may() {
		return 0, console.ErrWatching
	}
	return t.Terminal.Write(p)
}

func (t typist) Resize(size console.Size) error {
	if !t.may() {
		return nil
	}
	return t.Terminal.Resize(size)
}
