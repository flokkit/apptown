package httpapi

import (
	"context"
	"log"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/stamp"
)

// RoleCatalog is the role catalog and what each capability allows.
type RoleCatalog struct {
	Roles        []RoleView                `json:"roles"`
	Capabilities map[string]CapabilityView `json:"capabilities"`
}

// CapabilityView says what a capability allows and how risky it is.
type CapabilityView struct {
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

// GrantList is a set of grants as the People page shows them.
type GrantList struct {
	Grants []GrantView `json:"grants"`
}

type RoleView struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Surfaced    bool     `json:"surfaced"`
	Caps        []string `json:"capabilities"`
}

type GrantView struct {
	ID          ids.ID     `json:"id"`
	SubjectType string     `json:"subjectType"`
	Subject     string     `json:"subject"`
	Role        string     `json:"role,omitempty"`
	RoleTitle   string     `json:"roleTitle,omitempty"`
	Capability  string     `json:"capability,omitempty"`
	ScopeType   string     `json:"scopeType"`
	ScopeID     string     `json:"scopeId"`
	ScopeName   string     `json:"scopeName,omitempty"`
	Origin      string     `json:"origin"`
	GrantedBy   string     `json:"grantedBy"`
	Reason      string     `json:"reason,omitempty"`
	CreatedAt   string     `json:"createdAt"`
	ExpiresAt   stamp.Time `json:"expiresAt,omitzero"`
	ReviewAt    stamp.Time `json:"reviewAt,omitzero"`
}

func grantOutput(ctx context.Context, projects *project.Projects, g access.Grant) GrantView {
	o := GrantView{ID: g.ID, SubjectType: g.SubjectType, Subject: g.Subject, Role: g.RoleName,
		Capability: string(g.Capability), ScopeType: string(g.ScopeType), ScopeID: g.ScopeID, Origin: g.Origin,
		GrantedBy: g.GrantedBy, Reason: g.Reason, CreatedAt: g.CreatedAt, ExpiresAt: g.ExpiresAt, ReviewAt: g.ReviewAt}
	if rv := access.RoleByName(g.RoleName); rv != nil {
		o.RoleTitle = rv.Title
	}
	if g.ScopeType == access.ScopeProject {
		if id, err := ids.Parse(g.ScopeID); err == nil {
			if p := optionalProject(ctx, projects, id); p != nil {
				o.ScopeName = p.Name
			}
		}
	}
	return o
}

func optionalProject(ctx context.Context, projects *project.Projects, id ids.ID) *project.Project {
	p, err := projects.ByID(ctx, id)
	if err != nil {
		return nil
	}
	return &p
}
func optionalUser(ctx context.Context, authority *identity.Service, name string) string {
	role, err := authority.EnsureUser(ctx, name)
	if err != nil {
		log.Printf("recording user %s: %v", name, err)
		return "member"
	}
	return role
}
