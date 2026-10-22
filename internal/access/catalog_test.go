package access_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/access"
)

// The catalog is closed and consistent: every capability has a definition
// and a risk word, every role bundles only catalogued capabilities, the
// sovereign holds everything, and reads are the only thing that flows
// down from an instance or group grant (plus project:create to a group).
func TestCatalogIsConsistent(t *testing.T) {
	risks := map[string]bool{"read": true, "mutate": true, "approve": true, "release": true, "execute": true, "admin": true}
	for c, d := range access.Definitions {
		if d.Description == "" || !risks[d.Risk] {
			t.Errorf("%s: definition incomplete: %+v", c, d)
		}
		for _, s := range d.Inherits {
			if s != access.ScopeGroup && s != access.ScopeProject {
				t.Errorf("%s inherits to %s; only group and project are reachable from above", c, s)
			}
		}
		if d.Risk != "read" && len(d.Inherits) > 0 && c != access.CapProjectCreate {
			t.Errorf("%s is %s yet flows down: %v", c, d.Risk, d.Inherits)
		}
	}
	if !access.Inherits(access.CapProjectRead, access.ScopeProject) || !access.Inherits(access.CapProjectRead, access.ScopeGroup) {
		t.Error("a read granted on the instance reaches the projects it owns")
	}
	if access.Inherits(access.CapProjectCreate, access.ScopeProject) || !access.Inherits(access.CapProjectCreate, access.ScopeGroup) {
		t.Error("project:create flows instance to group and no further")
	}
	if access.Inherits(access.CapChangeLand, access.ScopeProject) || access.Inherits(access.Capability("nope"), access.ScopeProject) {
		t.Error("a mutate, or an unknown capability, never inherits")
	}
	all := access.AllCapabilities()
	if len(all) != len(access.Definitions) {
		t.Fatalf("access.AllCapabilities lists %d of %d", len(all), len(access.Definitions))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("catalog not sorted at %s", all[i])
		}
	}
	seen := map[string]bool{}
	for _, r := range access.StockRoles {
		if seen[r.Name] {
			t.Errorf("role %s defined twice", r.Name)
		}
		seen[r.Name] = true
		if r.Title == "" || r.Scope == "" {
			t.Errorf("role %s incomplete: %+v", r.Name, r)
		}
		for _, c := range r.Caps {
			if _, ok := access.Definitions[c]; !ok {
				t.Errorf("role %s bundles uncatalogued %s", r.Name, c)
			}
		}
	}
	sovereign := access.RoleByName(access.RoleInstanceAdmin)
	if sovereign == nil || len(sovereign.Caps) != len(access.Definitions) || !sovereign.Surfaced {
		t.Fatal("the instance administrator holds every capability and is surfaced")
	}
	if !access.RoleByName(access.RoleProjectApprover).Holds(access.CapChangeApprove) || access.RoleByName(access.RoleProjectApprover).Holds(access.CapChangeLand) {
		t.Error("an approver assents and does not land")
	}
	if access.RoleByName(access.RoleProjectContrib).Holds(access.CapChangeApprove) {
		t.Error("a contributor does not approve")
	}
	if access.RoleByName("nobody") != nil {
		t.Error("an unknown role is nil")
	}
}

// Project roles do not inherit instance administration, while the sovereign
// actually holds each catalogued capability (not just the same count).
func TestStockRoleAdministrativeBounds(t *testing.T) {
	for _, rv := range access.StockRoles {
		if rv.Scope != access.ScopeProject {
			continue
		}
		for _, c := range rv.Caps {
			if access.Definitions[c].Risk == "admin" && c != access.CapProjectManageMembers && c != access.CapProjectDelete {
				t.Errorf("project role %s carries admin capability %s", rv.Name, c)
			}
		}
	}
	sovereign := access.RoleByName(access.RoleInstanceAdmin)
	if sovereign == nil {
		t.Fatal("the sovereign role is missing")
	}
	for c := range access.Definitions {
		if !sovereign.Holds(c) {
			t.Errorf("the sovereign lacks %s", c)
		}
	}
}
