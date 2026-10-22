package identity

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// RequireEnabledIn preserves account-first refusal at domain preflights whose
// eligibility checks precede the capability decision. A disabled account
// and a name with no account are refused alike. access.Authority is still checked
// by Require; this read never grants permission.
func RequireEnabledIn(ctx context.Context, q database.Queryer, pr access.Principal) error {
	person := pr.Name
	if pr.Instrument == "console" {
		person = pr.Person
	}
	if pr.Kind != "user" && pr.Instrument != "console" {
		return nil
	}
	// An admitted principal is judged by its ID, so a rename keeps it and a
	// later account with its old name is someone else.
	disabled, err := PrincipalDisabledIn(ctx, q, access.Principal{ID: pr.ID, Kind: "user", Name: person})
	if err != nil {
		return err
	}
	if disabled {
		return &access.Denied{Code: access.DenyAccountDisabled, Reason: person + "'s account is disabled or does not exist"}
	}
	return nil
}

// SystemActor reports whether a name is one AppTown acts under itself —
// its engine, triggers and bindings ("system", "apptown-system", "forge"), the
// author of the commits it writes ("apptown"; "forge-system" and a "forge"
// author are the same actors in records from before the AppTown rename) or a run's
// own identity ("run/<id>", a UUID, or a number in history from before
// UUIDs) — rather than a person's. Such a name holds no account, and no
// person may take it (ValidName).
func SystemActor(name string) bool {
	if name == "system" || name == "apptown-system" || name == "apptown" || name == "forge-system" || name == "forge" {
		return true
	}
	id, ok := strings.CutPrefix(name, "run/")
	if !ok || id == "" {
		return false
	}
	if _, err := ids.Parse(id); err == nil {
		return true
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
