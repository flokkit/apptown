package identity

import (
	"context"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Approver is one entry of an approval's approver expressions with the
// people it reaches now, so a person stopped at an approval can see whom to
// ask. A group lists its members; a role lists the people and groups that
// hold it on the project. Everyone marks the built-in group of all users,
// whose members are not listed.
type Approver struct {
	Kind     string     `json:"kind"` // person, group or role
	Name     string     `json:"name"`
	Label    string     `json:"label"`
	People   []string   `json:"people"`
	Groups   []Approver `json:"groups,omitempty"`
	Everyone bool       `json:"everyone,omitempty"`
}

// ApproversIn resolves approver expressions ("name", "group:name",
// "role:name") for a project, live as of at. It reads grants and
// memberships the way approval eligibility does, so the list names the
// people who could decide now.
func ApproversIn(ctx context.Context, q database.Queryer, projectID ids.ID, expressions []string, at time.Time) ([]Approver, error) {
	out := make([]Approver, 0, len(expressions))
	for _, expr := range expressions {
		if name, ok := strings.CutPrefix(expr, "group:"); ok {
			g, err := groupApprover(ctx, q, name, at)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
			continue
		}
		if name, ok := strings.CutPrefix(expr, "role:"); ok {
			label := name
			if role := access.RoleByName(name); role != nil {
				label = role.Title
			}
			r := Approver{Kind: "role", Name: name, Label: label, People: []string{}}
			holders, err := queries(q).RoleHoldersAt(ctx, accesssql.RoleHoldersAtParams{At: at, ScopeType: string(access.ScopeProject), ScopeID: projectID.String(), Role: name})
			if err != nil {
				return nil, err
			}
			for _, h := range holders {
				if h.SubjectType == "group" {
					g, err := groupApprover(ctx, q, h.Subject, at)
					if err != nil {
						return nil, err
					}
					r.Groups = append(r.Groups, g)
				} else if h.SubjectType == "user" {
					r.People = append(r.People, h.Subject)
				}
			}
			out = append(out, r)
			continue
		}
		out = append(out, Approver{Kind: "person", Name: expr, Label: expr, People: []string{expr}})
	}
	return out, nil
}

func groupApprover(ctx context.Context, q database.Queryer, name string, at time.Time) (Approver, error) {
	g := Approver{Kind: "group", Name: name, Label: name, People: []string{}}
	if name == GroupAllUsers {
		g.Everyone = true
		return g, nil
	}
	members, err := queries(q).LiveGroupMembers(ctx, accesssql.LiveGroupMembersParams{GroupName: name, At: at})
	if err != nil {
		return Approver{}, err
	}
	g.People = append(g.People, members...)
	return g, nil
}
