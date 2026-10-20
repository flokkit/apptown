package identity

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// The shared policy is the same before and after setup. Setup may establish
// grants; its marker cannot admit ordinary commands or invent a caller.
func TestCheckedPolicyAcrossSetupStates(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "before-setup"
		if completed {
			name = "after-setup"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{OpenInstance: true})
			if completed {
				if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.EnsureUser(context.Background(), "boss"); err != nil {
				// boss has an account but no grant: its role claim alone is refused.
				t.Fatal(err)
			}
			for _, user := range []string{"owner", "outsider", "disabled"} {
				if _, err := s.EnsureUser(context.Background(), user); err != nil {
					t.Fatal(err)
				}
			}
			for _, spec := range []Spec{
				{SubjectType: "user", Subject: "owner", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(7).String()},
				{SubjectType: "user", Subject: "outsider", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(8).String()},
				{SubjectType: "user", Subject: "disabled", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(7).String()},
				{SubjectType: "anonymous", Subject: "anonymous", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: idstest.N(7).String()},
			} {
				spec.By = "fixture"
				if _, err := s.PutGrant(context.Background(), spec); err != nil {
					t.Fatal(err)
				}
			}
			disabled := true
			if err := testSaveAccount(s.db, accountEdit{Name: "disabled", Role: "member", Disabled: &disabled}, "fixture"); err != nil {
				t.Fatal(err)
			}
			checked := access.Checked{Authority: s, Projects: resourceFunc(func(context.Context, ids.ID) (access.Resource, error) {
				return access.Resource{Group: "team", ProjectName: "Seven"}, nil
			})}
			cases := []struct {
				name    string
				caller  access.Caller
				cap     access.Capability
				code    string
				missing bool
			}{
				{name: "missing", cap: access.CapChangeCreate, missing: true},
				{name: "unverified root label", caller: access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Role: "admin", Via: "session"}}, cap: access.CapChangeCreate, missing: true},
				{name: "project owner read", caller: access.Caller{Principal: testPrincipal("owner"), Verified: true}, cap: access.CapProjectRead},
				{name: "project owner write", caller: access.Caller{Principal: testPrincipal("owner"), Verified: true, Receipt: "owner via client"}, cap: access.CapChangeCreate},
				{name: "outside project", caller: access.Caller{Principal: testPrincipal("outsider"), Verified: true}, cap: access.CapChangeCreate, code: access.DenyOutsideScope},
				{name: "disabled first", caller: access.Caller{Principal: access.Principal{Kind: "user", Name: "disabled", Via: "oauth"}, Verified: true}, cap: access.CapChangeCreate, code: access.DenyAccountDisabled},
				{name: "oauth narrowed", caller: access.Caller{Principal: access.Principal{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}, Verified: true}, cap: access.CapChangeCreate, code: access.DenyDelegationNarrow},
				{name: "explicit anonymous read", caller: access.Caller{Principal: nobody, Verified: true}, cap: access.CapProjectRead},
				{name: "anonymous cannot write", caller: access.Caller{Principal: nobody, Verified: true}, cap: access.CapChangeCreate, code: access.DenyMissingCapability},
				{name: "run evidence", caller: access.Caller{Principal: access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, Verified: true}, cap: access.CapEvidenceAttach},
				{name: "run cannot approve", caller: access.Caller{Principal: access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, Verified: true}, cap: access.CapChangeApprove, code: access.DenyDelegationNarrow},
				{name: "console disabled owner", caller: access.Caller{Principal: access.Principal{Kind: "instrument", Name: "disabled via console/c", Person: "disabled", Instrument: "console", ProjectID: idstest.N(7)}, Verified: true}, cap: access.CapProjectRead, code: access.DenyAccountDisabled},
				{name: "console cannot approve", caller: access.Caller{Principal: access.Principal{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", ProjectID: idstest.N(7)}, Verified: true}, cap: access.CapChangeApprove, code: access.DenyDelegationNarrow},
			}
			rootCode := ""
			if !completed {
				rootCode = access.DenyMissingCapability
			}
			cases = append(cases, struct {
				name    string
				caller  access.Caller
				cap     access.Capability
				code    string
				missing bool
			}{name: "root comes from grants, not role claim", caller: access.Caller{Principal: testPrincipal("boss"), Verified: true}, cap: access.CapChangeApprove, code: rootCode})
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := checked.Require(access.WithCaller(context.Background(), tc.caller), access.Permission{Capability: tc.cap, Resource: access.Resource{Type: "project", ID: idstest.N(7).String(), ProjectID: idstest.N(7)}})
					if tc.missing {
						if !errors.Is(err, access.ErrPrincipalRequired) {
							t.Fatalf("missing provenance: %v", err)
						}
						return
					}
					if tc.code != "" {
						var denied *access.Denied
						if !errors.As(err, &denied) || denied.Code != tc.code {
							t.Fatalf("want %s: %v", tc.code, err)
						}
						return
					}
					if err != nil || got.Principal.Name != tc.caller.Principal.Name {
						t.Fatalf("allowed caller: %+v %v", got, err)
					}
					if tc.caller.Receipt != "" && got.Receipt != tc.caller.Receipt {
						t.Fatal("receipt replaced identity")
					}
				})
			}
		})
	}
}
