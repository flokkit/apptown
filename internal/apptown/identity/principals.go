package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// personIn is the live person principal named name, made now if no live
// principal holds the name. A name a removed principal held gets a new
// principal, which inherits nothing. Two callers making the same name at
// once agree: the second insert waits for the first and then reads it.
func personIn(ctx context.Context, q database.Queryer, name string, at time.Time) (ids.ID, error) {
	id, err := queries(q).InsertPersonPrincipal(ctx, accesssql.InsertPersonPrincipalParams{ID: ids.New(at.UTC()), Name: name, At: at.UTC()})
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, err
	}
	return queries(q).LivePersonByName(ctx, name)
}

// recordPersonIn records the account of a person a grant or a membership
// names, with its principal, when there is none yet; it is not a sighting.
func recordPersonIn(ctx context.Context, tx *database.Tx, name string, at time.Time) error {
	person, err := personIn(ctx, tx, name, at)
	if err != nil {
		return err
	}
	return queries(tx).RecordUser(ctx, accesssql.RecordUserParams{Username: name, PrincipalID: person, CreatedAt: at.UTC()})
}

// ErrNameTaken is a rename to a name a live principal holds.
var ErrNameTaken = fault.New(fault.Conflict, "that name is taken")

func renameIn(ctx context.Context, tx *database.Tx, from, to, by string, at time.Time) error {
	if err := ValidName(to); err != nil {
		return err
	}
	q := queries(tx)
	if _, err := q.LivePersonByName(ctx, to); err == nil {
		return fmt.Errorf("%w: %s", ErrNameTaken, to)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	person, err := q.RenameUser(ctx, accesssql.RenameUserParams{NewName: to, OldName: from})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNoSuchName, from)
	}
	if err != nil {
		return err
	}
	if _, err := q.RenamePrincipal(ctx, accesssql.RenamePrincipalParams{ID: person, Name: to}); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, by, ids.Nil, "user.renamed", to, map[string]any{"from": from, "principal": person})
}

// ActorIn is the actor an act by name is recorded against (access.ActorFrom),
// with the principal of the live account of that name when the caller did
// not carry one: the account the name is signed in as now, at the act. A
// name that is no person's (an instrument's, AppTown's) stays unresolved.
func ActorIn(ctx context.Context, q database.Queryer, name string) (access.Actor, error) {
	a := access.ActorFrom(ctx, name)
	if !a.Principal.IsZero() || ValidName(name) != nil {
		return a, nil
	}
	id, err := queries(q).LivePersonByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return access.Actor{}, err
	}
	a.Principal = id
	return a, nil
}

// ShowActorsIn is how each actor reads in history (access.Actor.Shown), by
// the people they name as they stand now.
func ShowActorsIn(ctx context.Context, q database.Queryer, actors []access.Actor) ([]string, error) {
	var want []ids.ID
	for _, a := range actors {
		if !a.Principal.IsZero() {
			want = append(want, a.Principal)
		}
	}
	people := map[ids.ID]access.Person{}
	if len(want) > 0 {
		rows, err := queries(q).PrincipalsNow(ctx, want)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			people[r.ID] = access.Person{Known: true, Name: r.Name, Removed: r.Removed, Disabled: r.Disabled}
		}
	}
	out := make([]string, len(actors))
	for i, a := range actors {
		out[i] = a.Shown(people[a.Principal], SystemActor)
		if a.Principal.IsZero() && ownName(a.Name) {
			out[i] = "AppTown"
		}
	}
	return out, nil
}

// ownName reports whether a system actor name is AppTown itself (its engine,
// triggers, bindings and commit author, under its names before and after the
// AppTown rename), which history shows as "AppTown". The recorded name is
// kept: it is an identifier in authority and audit records. A run's identity
// is a system actor too, but not AppTown's own name, and reads as written.
func ownName(name string) bool {
	switch name {
	case "system", "apptown-system", "apptown", "forge-system", "forge":
		return true
	}
	return false
}

// PersonNameIn is the account name of the person, today; "" for a principal
// with no account (removed, or never one).
func PersonNameIn(ctx context.Context, q database.Queryer, person ids.ID) (string, error) {
	u, err := queries(q).UserByPrincipal(ctx, person)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return u.Username, err
}
