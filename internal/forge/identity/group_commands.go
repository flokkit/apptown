package identity

import (
	"context"
	"errors"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
)

// GroupChange replaces a group's description and whole member list.
type GroupChange struct {
	Name, Description string
	Members           []string
}

var ErrGroupName = fault.New(fault.Invalid, "group name (slug) required")

func (in GroupChange) prepare() (Group, error) {
	if !directoryName.MatchString(in.Name) {
		return Group{}, ErrGroupName
	}
	g := Group{Name: in.Name, Description: in.Description}
	err := g.replaceMembers(in.Members)
	return g, err
}

// replaceMembers keeps slug names in request order, including repeats for the
// receipt; the store persists a set and the caller's slice is never shared.
// A name that is not a usable person's name (ValidName: one of the forge's
// own names, a chat or run) refuses the whole change rather than being
// dropped; blanks and other non-slugs are dropped as before.
func (g *Group) replaceMembers(names []string) error {
	g.Members = nil
	for _, name := range names {
		if !directoryName.MatchString(name) {
			continue
		}
		if err := ValidName(name); err != nil {
			return err
		}
		g.Members = append(g.Members, name)
	}
	return nil
}

// SaveGroup records member names, the group, its whole membership and receipt
// together. Existing group kinds are preserved; changing their policy is a
// separate operation. Access checks the verified caller in the same transaction.
func (s *Service) SaveGroup(ctx context.Context, in GroupChange, pr access.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g, err := in.prepare()
	if err != nil {
		return err
	}
	// Consistent account-lock order also handles two groups sharing members.
	names := slices.Clone(g.Members)
	slices.Sort(names)
	names = slices.Compact(names)
	return s.administer(ctx, pr, access.CapIdentityGroups, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		for _, name := range names {
			person, err := personIn(ctx, tx, name, now)
			if err != nil {
				return err
			}
			if _, err := q.TouchUser(ctx, accesssql.TouchUserParams{Username: name, PrincipalID: person, At: now.UTC()}); err != nil {
				return err
			}
		}
		if _, err := q.SaveGroup(ctx, accesssql.SaveGroupParams{Name: g.Name, Description: g.Description, CreatedAt: now.UTC()}); err != nil {
			return err
		}
		if err := clearGroupMembersIn(ctx, tx, g.Name); err != nil {
			return err
		}
		for _, m := range names {
			if _, err := q.InsertMembership(ctx, accesssql.InsertMembershipParams{GroupName: g.Name, Username: m, CreatedAt: now.UTC()}); err != nil {
				return err
			}
		}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "group.saved", g.Name, map[string]any{"members": g.Members})
	})
}

// Clearing a member list is required work, including when deleting its group.
// A trigger that skips a row must not turn a partial removal into success.
func clearGroupMembersIn(ctx context.Context, tx *database.Tx, name string) error {
	gone, err := queries(tx).DeleteGroupMemberships(ctx, name)
	if err != nil {
		return err
	}
	if gone.Selected != gone.Removed {
		return errors.New("group memberships were not removed")
	}
	return nil
}
