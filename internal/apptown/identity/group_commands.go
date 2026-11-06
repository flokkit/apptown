package identity

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// GroupChange replaces a group's description and whole member list.
type GroupChange struct {
	Name, Description string
	Members           []string
}

var errGroupName = fault.New(fault.Invalid, "group name (slug) required")

func (in GroupChange) prepare() (Group, error) {
	if !directoryName.MatchString(in.Name) {
		return Group{}, errGroupName
	}
	g := Group{Name: in.Name, Description: in.Description}
	err := g.replaceMembers(in.Members)
	return g, err
}

// replaceMembers keeps slug names in request order, including repeats for the
// receipt; the store persists a set and the caller's slice is never shared.
// A name that is not a usable person's name (ValidName: one of AppTown's
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

// AddGroupMember adds one existing account without replacing other memberships.
// The current caller needs identity:manage-groups in the write transaction.
func (s *Service) AddGroupMember(ctx context.Context, name, username string, pr access.Principal) error {
	return s.changeGroupMember(ctx, name, username, pr, true)
}

// RemoveGroupMember removes only the named membership, preserving the group,
// its description and every other member. It has the same authority as SaveGroup.
func (s *Service) RemoveGroupMember(ctx context.Context, name, username string, pr access.Principal) error {
	return s.changeGroupMember(ctx, name, username, pr, false)
}

func (s *Service) changeGroupMember(ctx context.Context, name, username string, pr access.Principal, add bool) error {
	if !directoryName.MatchString(name) {
		return errGroupName
	}
	if err := ValidName(username); err != nil {
		return err
	}
	return s.administer(ctx, pr, access.CapIdentityGroups, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		g, err := q.GroupByNameForUpdate(ctx, name)
		found, err := database.Lookup(err)
		if err != nil {
			return err
		}
		if !found || g.RetiredAt.Valid {
			return errNoSuchName
		}
		if g.Kind == "built-in" || g.Kind == "personal" {
			return fault.New(fault.Conflict, "members of a built-in or personal group cannot be changed here")
		}
		u, err := q.UserByName(ctx, username)
		found, err = database.Lookup(err)
		if err != nil {
			return err
		}
		if !found {
			return errNoSuchName
		}
		var changed int64
		event := "group.member-removed"
		if add {
			changed, err = q.AddGroupMember(ctx, accesssql.AddGroupMemberParams{GroupName: name, PrincipalID: u.PrincipalID, CreatedBy: pr.Name, CreatedByPrincipal: pr.ID, CreatedByVia: pr.Via, CreatedAt: now.UTC()})
			event = "group.member-added"
		} else {
			changed, err = q.RemoveGroupMember(ctx, accesssql.RemoveGroupMemberParams{GroupName: name, PrincipalID: u.PrincipalID})
		}
		if err != nil {
			return err
		}
		if changed == 0 {
			return nil
		}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, event, name, map[string]any{"member": username})
	})
}
