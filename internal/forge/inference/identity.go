package inference

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/conversation"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow"
)

// Identity checks the current lifecycle and account authority of an admitted workload.
type Identity struct {
	DB      *database.DB
	Console *console.Service
	Tokens  *identity.RuntimeTokens
}

func (a Identity) Authenticate(ctx context.Context, token string) (identity.Workload, error) {
	if token == "" {
		return identity.Workload{}, errDenied
	}
	var who identity.Workload
	if s, ok := a.Console.Identity(token); ok {
		scope := conversation.FromLabels(s.Labels)
		opener, _ := ids.Parse(s.Labels[conversation.OpenerLabel])
		who = identity.Workload{Kind: "console", Actor: s.Actor, Principal: opener, ProjectID: scope.ProjectID, SessionID: s.SessionID, RunID: scope.RunID, StepID: scope.StepID, Attempt: scope.Attempt}
	} else if _, _, ok := a.Tokens.StepAttempt(token); ok {
		return a.AuthenticateStep(ctx, token)
	} else {
		// A process-local assist grant. It names no run; one that did would
		// bypass the step checks, so it is refused. One asked through a
		// console lasts only while that console does.
		who, ok = a.Tokens.Lookup(token)
		if !ok || !who.RunID.IsZero() || (who.SessionID != "" && !a.Console.Live(who.SessionID)) {
			return identity.Workload{}, errDenied
		}
	}
	return a.admitAccount(ctx, who)
}

// AuthenticateStep is the one validation of a step attempt's runtime token,
// used by both doors it opens: the inference gateway (through Authenticate)
// and the API's auth middleware. The MAC proves which attempt the token was
// issued for; admission then reads current state: the run is running, the
// attempt is the step's live attempt, and the run owner's account is
// enabled. A valid MAC alone opens neither door, and a read failure refuses.
func (a Identity) AuthenticateStep(ctx context.Context, token string) (identity.Workload, error) {
	stepID, attempt, ok := a.Tokens.StepAttempt(token)
	if !ok {
		return identity.Workload{}, errDenied
	}
	run, observed, err := workflow.ObserveStepAttemptIn(ctx, a.DB, stepID, attempt)
	if err != nil || observed.State.RequireRunning() != nil || !observed.LiveAttempt {
		return identity.Workload{}, errDenied
	}
	return a.admitAccount(ctx, identity.Workload{Kind: "step", Actor: observed.Owner, Principal: observed.OwnerPrincipal, ProjectID: run.ProjectID, RunID: run.ID, StepID: stepID, Attempt: attempt})
}

var errDenied = fmt.Errorf("unknown or inactive runtime credential")

func (a Identity) admitAccount(ctx context.Context, who identity.Workload) (identity.Workload, error) {
	if who.Actor == "" {
		who.Actor = "system"
	}
	// A workload acting for a person is checked as that person, whatever
	// its attribution says: a disabled account and a name with no account
	// are refused alike. Only AppTown's own names (identity.SystemActor)
	// act without an account. SQL failures fail closed.
	account := who.Actor
	if who.Person != "" {
		account = who.Person
	}
	if identity.SystemActor(account) {
		return who, nil
	}
	// The person is the principal the work was started or opened by, so a
	// rename keeps it and a later account with the old name lends nothing.
	// A person's workload with no principal is refused: its name could now
	// be someone else's.
	if who.Principal.IsZero() {
		return who, errDenied
	}
	if disabled, err := identity.PrincipalDisabledIn(ctx, a.DB, access.Principal{ID: who.Principal, Kind: "user", Name: account}); err != nil || disabled {
		return who, errDenied
	}
	return who, nil
}

// Legacy audit callers decorate user names with their delegated client.
// Explicit STEP_ secrets or attached integration settings must not reintroduce
// a configured provider credential through an alias. This is a canary guard
// on known delivery surfaces, not a claim to detect arbitrary secret copies.
func containsInferenceCredential(value string, p *inferenceProvider) bool {
	if p.key != "" && strings.Contains(value, p.key) {
		return true
	}
	for k, v := range p.headers {
		if (strings.Contains(strings.ToLower(k), "key") || strings.EqualFold(k, "Authorization")) && v != "" && strings.Contains(value, v) {
			return true
		}
	}
	return false
}
