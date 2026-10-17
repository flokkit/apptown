package console

import (
	"context"
	"errors"
)

// Placed runs each session's agent on one of two runtimes by its recorded
// placement: a chat on a worker has its runtime there (Remote), every other
// on this machine (Local). The placement is read from the session's record,
// live or ended, so an ended chat's runtime is still asked where it ran:
// only the runtime a chat ran on can say it is gone.
type Placed struct {
	Local, Remote Agent
	// OnRemote reports whether a session's record places it on the remote
	// runtime; an error is unknown, and the call fails rather than asking
	// the wrong runtime.
	OnRemote func(id string) (bool, error)
}

var _ Agent = Placed{}

func (p Placed) of(id string) (Agent, error) {
	remote, err := p.OnRemote(id)
	if err != nil {
		return nil, err
	}
	if remote {
		return p.Remote, nil
	}
	return p.Local, nil
}

func (p Placed) Start(ctx context.Context, in Launch) error {
	a, err := p.of(in.ID)
	if err != nil {
		return err
	}
	return a.Start(ctx, in)
}

func (p Placed) Stale(ctx context.Context, in Launch) (bool, error) {
	a, err := p.of(in.ID)
	if err != nil {
		return false, err
	}
	return a.Stale(ctx, in)
}

func (p Placed) Status(ctx context.Context, id string) (Status, error) {
	a, err := p.of(id)
	if err != nil {
		return Status{}, err
	}
	return a.Status(ctx, id)
}

func (p Placed) Deliver(ctx context.Context, in Launch, text string) error {
	a, err := p.of(in.ID)
	if err != nil {
		return err
	}
	return a.Deliver(ctx, in, text)
}

func (p Placed) Interrupt(ctx context.Context, id string) error {
	a, err := p.of(id)
	if err != nil {
		return err
	}
	return a.Interrupt(ctx, id)
}

func (p Placed) Attach(ctx context.Context, id string, size Size) (Terminal, error) {
	a, err := p.of(id)
	if err != nil {
		return nil, err
	}
	return a.Attach(ctx, id, size)
}

func (p Placed) Stop(ctx context.Context, id string) error {
	a, err := p.of(id)
	if err != nil {
		return err
	}
	return a.Stop(ctx, id)
}

func (p Placed) Remove(ctx context.Context, id string) error {
	a, err := p.of(id)
	if err != nil {
		return err
	}
	return a.Remove(ctx, id)
}

func (p Placed) End(ctx context.Context, id string) error {
	a, err := p.of(id)
	if err != nil {
		return err
	}
	return a.End(ctx, id)
}

// Reap asks both runtimes to remove what sessions not alive left behind.
// The remote runtime is told only of the sessions placed on it (or whose
// placement cannot be read), since it resumes watching each one it is told
// of.
func (p Placed) Reap(ctx context.Context, alive map[string]bool) error {
	remote := map[string]bool{}
	for id := range alive {
		if on, err := p.OnRemote(id); err != nil || on {
			remote[id] = true
		}
	}
	return errors.Join(p.Local.Reap(ctx, alive), p.Remote.Reap(ctx, remote))
}

// ErrNoLocalChats refuses running a chat on a forge that runs none.
var ErrNoLocalChats = errors.New("this chat ran on the forge, which no longer runs chats")

// NoLocal is the local runtime of a forge that runs no chats
// (-no-local-docker). That forge's start was refused while a chat placed on
// it had not ended, so nothing of one is here: its status is absent, and
// stopping, removing, ending and reaping have nothing to do. Waking one is
// refused (ErrNoLocalChats); its record and transcript stay.
type NoLocal struct{}

var _ Agent = NoLocal{}

func (NoLocal) Start(context.Context, Launch) error                    { return ErrNoLocalChats }
func (NoLocal) Stale(context.Context, Launch) (bool, error)            { return false, ErrNoLocalChats }
func (NoLocal) Status(context.Context, string) (Status, error)         { return Status{}, nil }
func (NoLocal) Deliver(context.Context, Launch, string) error          { return ErrNoLocalChats }
func (NoLocal) Interrupt(context.Context, string) error                { return ErrNoLocalChats }
func (NoLocal) Attach(context.Context, string, Size) (Terminal, error) { return nil, ErrNoLocalChats }
func (NoLocal) Stop(context.Context, string) error                     { return nil }
func (NoLocal) Remove(context.Context, string) error                   { return nil }
func (NoLocal) End(context.Context, string) error                      { return nil }
func (NoLocal) Reap(context.Context, map[string]bool) error            { return nil }
