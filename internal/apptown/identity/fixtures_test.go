package identity

import (
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The principals, resources and grant shorthand the policy tests share. The
// rule's own table lives with the rule (internal/access); these are the copies
// identity's tests need now that the two are separate packages.
var (
	alice  = access.Principal{Kind: "user", Name: "alice", Via: "session"}
	nobody = access.Principal{Kind: "anonymous", Name: "anonymous"}
	change = access.Resource{Type: "change", ID: "40", ProjectID: idstest.N(7), Group: "kids", ChangeID: idstest.N(40)}
	full   = access.Channel{Kind: "full-breadth"}
)

func role(id ids.ID, subjectType, subject, name string, scope access.ScopeKind, scopeID string) access.Grant {
	return access.Grant{ID: id, SubjectType: subjectType, Subject: subject, RoleName: name, ScopeType: scope, ScopeID: scopeID}
}

type fakeFacts struct {
	disabled map[string]bool
	grants   map[string][]access.Grant // by principal name
	reads    []string
}

func (f *fakeFacts) Disabled(pr access.Principal) bool {
	f.reads = append(f.reads, "disabled")
	return f.disabled[pr.Name]
}
func (f *fakeFacts) Grants(pr access.Principal) []access.Grant {
	f.reads = append(f.reads, "grants:"+pr.Name)
	return f.grants[pr.Name]
}
