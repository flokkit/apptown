package access_test

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids/idstest"
)

// fakeFacts is a database-free reading of the world for one decision.
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

var (
	p7, p8  = idstest.N(7).String(), idstest.N(8).String()
	alice   = access.Principal{Kind: "user", Name: "alice", Via: "session"}
	nobody  = access.Principal{Kind: "anonymous", Name: "anonymous"}
	project = access.Resource{Type: "project", ID: p7, ProjectID: idstest.N(7), Group: "kids"}
	change  = access.Resource{Type: "change", ID: idstest.N(40).String(), ProjectID: idstest.N(7), Group: "kids", ChangeID: idstest.N(40)}
	inst    = access.Resource{Type: "instance"}
	full    = access.Channel{Kind: "full-breadth"}

	agentToken = access.Principal{Kind: "user", Name: "alice", Via: "token", Role: "admin", Purpose: access.TokenAgent}
	cliToken   = access.Principal{Kind: "user", Name: "alice", Via: "token", Role: "admin", Purpose: access.TokenCLI}
	// memberCLIToken: alice is an administrator, her token was made member.
	memberCLIToken = access.Principal{Kind: "user", Name: "alice", Via: "token", Role: "member", Purpose: access.TokenCLI}
	agentChannel   = access.DelegationOf(agentToken)
	rootFacts      = &fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}}
)

func role(n uint64, subjectType, subject, name string, scope access.ScopeKind, scopeID string) access.Grant {
	return access.Grant{ID: idstest.N(n), SubjectType: subjectType, Subject: subject, RoleName: name, ScopeType: scope, ScopeID: scopeID}
}

// §12's order, row by row.
func TestDecideTable(t *testing.T) {
	cases := []struct {
		name   string
		pr     access.Principal
		d      access.Channel
		c      access.Capability
		res    access.Resource
		ctx    access.Context
		facts  *fakeFacts
		allow  bool
		code   string
		reason string // a fragment
	}{
		{"unknown capability", alice, full, access.Capability("nope:x"), inst, access.Context{}, &fakeFacts{}, false, access.DenyMissingCapability, "not a capability"},
		{"disabled account fails first", alice, full, access.CapProjectRead, project, access.Context{},
			&fakeFacts{disabled: map[string]bool{"alice": true}, grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}},
			false, access.DenyAccountDisabled, "disabled"},
		{"oauth scopes only subtract", access.Principal{Kind: "user", Name: "alice", Via: "oauth", Scopes: []string{"apptown:user:read"}}, access.Channel{Kind: "oauth", ID: "3"}, access.CapChangeCreate, project, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectOwner, access.ScopeProject, p7)}}},
			false, access.DenyDelegationNarrow, "reconnect"},
		{"any oauth scope may read", access.Principal{Kind: "user", Name: "alice", Via: "oauth", Scopes: []string{"apptown:change:check"}}, access.Channel{Kind: "oauth", ID: "3"}, access.CapProjectRead, project, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectViewer, access.ScopeProject, p7)}}},
			true, "", "Project viewer"},
		{"a run holds exactly its table", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapEvidenceAttach, project, access.Context{}, &fakeFacts{}, true, "", "step agent"},
		{"a run may not approve", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeApprove, change, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"an instrument stays in its project", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(8)}, access.Channel{Kind: "instrument"}, access.CapProjectRead, project, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "another project"},
		{"a run reads nothing instance-wide outside its table", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapAuditRead, inst, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"a run does not read the people", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapInstitutionRead, inst, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"a run reads instance lists its table names", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapProjectRead, inst, access.Context{}, &fakeFacts{}, true, "", "step agent"},
		{"a run still reads its own project's records", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapAuditRead, project, access.Context{}, &fakeFacts{}, true, "", "step agent"},
		{"a run may not open a change", access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"AppTown's trigger opens a change", access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{}, &fakeFacts{}, true, "", "AppTown's own trigger"},
		{"AppTown's trigger starts a run", access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "webhook", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeInvokeLane, change, access.Context{}, &fakeFacts{}, true, "", "AppTown's own trigger"},
		{"AppTown's engine advances a run", access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "engine", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapRunOperate, change, access.Context{}, &fakeFacts{}, true, "", "AppTown's own trigger"},
		{"AppTown's trigger may not land", access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeLand, change, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"AppTown's trigger stays in its project", access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: idstest.N(8)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "another project"},
		{"the instrument name from before the AppTown rename carries nothing", access.Principal{Kind: "instrument", Name: "system", Instrument: "forge", Via: "schedule", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{}, &fakeFacts{}, false, access.DenyDelegationNarrow, "needs a person"},
		{"a console acts as its person", access.Principal{Kind: "instrument", Name: "alice via console/x", Person: "alice", Instrument: "console", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectContrib, access.ScopeProject, p7)}}},
			true, "", "Project contributor"},
		{"a console's person without the grant", access.Principal{Kind: "instrument", Name: "bob via console/x", Person: "bob", Instrument: "console", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{},
			&fakeFacts{}, false, access.DenyMissingCapability, "bob may not open a change on this project"},
		{"root acts everywhere", alice, full, access.CapChangeLand, change, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}},
			true, "", "Instance administrator"},
		{"root reaches project mutations", alice, full, access.CapChangeLand, change, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}},
			true, "", "Instance administrator"},
		{"root reaches project reads", alice, full, access.CapChangeRead, change, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}},
			true, "", "Instance administrator"},
		{"a project role on its own project", alice, full, access.CapChangeLand, change, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectMaintainer, access.ScopeProject, p7)}}},
			true, "", "Project maintainer"},
		{"a project role on another project", alice, full, access.CapChangeLand, access.Resource{Type: "project", ID: p8, ProjectID: idstest.N(8)}, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectMaintainer, access.ScopeProject, p7)}}},
			false, access.DenyOutsideScope, "not here"},
		{"a group grant reaches its projects' reads", alice, full, access.CapProjectRead, project, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "group", "kids", access.RoleGroupAuditor, access.ScopeGroup, "kids")}}},
			true, "", "through group kids"},
		{"a group grant does not reach a project mutate", alice, full, access.CapChangeCreate, project, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "group", "kids", access.RoleGroupOwner, access.ScopeGroup, "kids")}}},
			false, access.DenyMissingCapability, "Project contributor"},
		{"a direct capability grant", alice, full, access.CapReleasePromote, access.Resource{Type: "release-target", ID: "prod", ProjectID: idstest.N(7)}, access.Context{},
			&fakeFacts{grants: map[string][]access.Grant{"alice": {{ID: idstest.N(5), SubjectType: "user", Subject: "alice", Capability: access.CapReleasePromote, ScopeType: access.ScopeTarget, ScopeID: "*"}}}},
			true, "", "a direct grant"},
		// A personal token's purpose: an agent token is bounded like an
		// OAuth connection holding every scope, before root is consulted; a
		// cli token acts as its person.
		{"an administrator's agent token may not approve", agentToken, agentChannel, access.CapChangeApprove, change, access.Context{}, rootFacts, false, access.DenyDelegationNarrow, "agent token may not"},
		{"an administrator's agent token may not land", agentToken, agentChannel, access.CapChangeLand, change, access.Context{}, rootFacts, false, access.DenyDelegationNarrow, "agent token may not"},
		{"an administrator's agent token may not administer", agentToken, agentChannel, access.CapIdentityAuth, inst, access.Context{}, rootFacts, false, access.DenyDelegationNarrow, "agent token may not"},
		{"an agent token does what a scope covers", agentToken, agentChannel, access.CapChangeRequestLand, change, access.Context{}, rootFacts, true, "", "Instance administrator"},
		{"an administrator's cli token approves", cliToken, access.Channel{Kind: "legacy-token"}, access.CapChangeApprove, change, access.Context{}, rootFacts, true, "", "Instance administrator"},
		// A token made without administrator authority does not carry its
		// administrator's root; the refusal says so and how to get one,
		// never "ask an administrator" (one-server test, 22 Oct).
		{"an administrator's member cli token is told the token is the limit", memberCLIToken, access.Channel{Kind: "legacy-token"}, access.CapReleasePromote, change, access.Context{}, rootFacts, false, access.DenyMissingCapability, "the token was made without administrator authority, which alice holds; sign in on the web and make a command-line token on People"},
		{"an administrator's member cli token on a root-only act", memberCLIToken, access.Channel{Kind: "legacy-token"}, access.CapIdentityAuth, inst, access.Context{}, rootFacts, false, access.DenyMissingCapability, "the token was made without administrator authority"},
		{"anonymous with nothing", nobody, access.Channel{Kind: "anonymous"}, access.CapProjectRead, project, access.Context{}, &fakeFacts{}, false, access.DenyMissingCapability, "Someone who is not signed in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := access.Decide(tc.pr, tc.d, tc.c, tc.res, tc.ctx, tc.facts)
			if dec.Allow != tc.allow || dec.Code != tc.code || !strings.Contains(dec.Reason, tc.reason) {
				t.Fatalf("got allow=%v code=%q reason=%q; want allow=%v code=%q reason containing %q", dec.Allow, dec.Code, dec.Reason, tc.allow, tc.code, tc.reason)
			}
		})
	}
}

// The decision names what it rested on: grant ids, roles at their
// names, and the group path when authority came through a group.
func TestDecisionNamesItsGrounds(t *testing.T) {
	facts := &fakeFacts{grants: map[string][]access.Grant{"alice": {
		role(11, "group", "kids", access.RoleGroupAuditor, access.ScopeGroup, "kids"),
		role(12, "user", "alice", access.RoleProjectViewer, access.ScopeProject, p7),
		role(13, "user", "alice", access.RoleProjectOwner, access.ScopeProject, p8), // elsewhere
	}}}
	dec := access.Decide(alice, full, access.CapProjectRead, project, access.Context{}, facts)
	if !dec.Allow || len(dec.GrantIDs) != 2 || dec.GrantIDs[0] != idstest.N(11) || dec.GrantIDs[1] != idstest.N(12) {
		t.Fatalf("grounds: %+v", dec)
	}
	if strings.Join(dec.Roles, ",") != access.RoleGroupAuditor+","+access.RoleProjectViewer || strings.Join(dec.Paths, ",") != "group:kids" {
		t.Fatalf("roles and paths: %+v", dec)
	}
}

// Reads happen only when the rule reaches them: no grants for a run token, and grants for the
// person behind a console, not the console.
func TestDecideReadsOnlyWhatItNeeds(t *testing.T) {
	facts := &fakeFacts{grants: map[string][]access.Grant{"alice": {role(1, "user", "alice", access.RoleProjectApprover, access.ScopeProject, p7)}}}
	access.Decide(alice, full, access.CapChangeApprove, change, access.Context{}, facts)
	if strings.Join(facts.reads, " ") != "disabled grants:alice" {
		t.Fatalf("reads for a plain approval: %v", facts.reads)
	}
	facts.reads = nil
	access.Decide(access.Principal{Kind: "instrument", Name: "run/9", Instrument: "run", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapProjectRead, project, access.Context{}, facts)
	if len(facts.reads) != 0 {
		t.Fatalf("a run token reads nothing: %v", facts.reads)
	}
	access.Decide(access.Principal{Kind: "instrument", Name: "alice via console/x", Person: "alice", Instrument: "console", ProjectID: idstest.N(7)}, access.Channel{Kind: "instrument"}, access.CapChangeCreate, project, access.Context{}, facts)
	if strings.Join(facts.reads, " ") != "grants:alice" {
		t.Fatalf("a console reads its person's grants: %v", facts.reads)
	}
}

func TestDelegationOf(t *testing.T) {
	if access.DelegationOf(alice).Kind != "full-breadth" || access.DelegationOf(nobody).Kind != "anonymous" ||
		access.DelegationOf(access.Principal{Kind: "user", Name: "a", Via: "token"}).Kind != "legacy-token" ||
		access.DelegationOf(cliToken).Kind != "legacy-token" || access.DelegationOf(agentToken).Kind != "agent-token" ||
		access.DelegationOf(access.Principal{Kind: "user", Name: "a", Via: "oauth", DelegationID: idstest.N(4)}).ID != idstest.N(4).String() ||
		access.DelegationOf(access.Principal{Kind: "instrument", Name: "run/1"}).ID != "run/1" {
		t.Fatal("delegation words wrong")
	}
}

// Consent scopes: any scope may read; a write needs its own scope; the
// vocabulary is closed.
func TestScopes(t *testing.T) {
	if !access.ScopesCover([]string{"apptown:change:check"}, access.CapProjectRead) || access.ScopesCover(nil, access.CapProjectRead) {
		t.Fatal("reads are covered by any scope, and by none")
	}
	if access.ScopesCover([]string{"apptown:change:check"}, access.CapChangeCreate) || !access.ScopesCover([]string{"apptown:change:create"}, access.CapChangeUpdateBrief) {
		t.Fatal("writes need their scope")
	}
	if len(access.ScopeNames()) != len(access.Scopes) || access.ScopeMeans("apptown:user:read") == "" || access.ScopeMeans("nope") != "" {
		t.Fatal("scope vocabulary")
	}
	for _, s := range access.Scopes {
		for _, c := range s.Caps {
			if _, ok := access.Definitions[c]; !ok {
				t.Errorf("scope %s names uncatalogued %s", s.Name, c)
			}
		}
	}
}

// An agent's reach is the scope catalogue, for OAuth connections and agent
// tokens alike, so no scope may carry an act that needs a person: assent,
// landing, or anything about the instance itself.
func TestNoScopeCoversAnActThatNeedsAPerson(t *testing.T) {
	for _, s := range access.Scopes {
		for _, c := range s.Caps {
			risk := access.Definitions[c].Risk
			if risk == "read" {
				continue
			}
			if risk != "mutate" || c == access.CapChangeLand {
				t.Errorf("scope %s carries %s, which needs a person", s.Name, c)
			}
		}
	}
}

func TestInstrumentTable(t *testing.T) {
	if !access.InstrumentMay("run", access.CapRunRead) || !access.InstrumentMay("console", access.CapChangeRequestLand) || access.InstrumentMay("run", access.CapChangeRequestLand) || access.InstrumentMay("console", access.CapChangeApprove) {
		t.Fatal("§11 table")
	}
}

// Putting a copy's data into a deployment, and opening a restored copy of
// production, are a person's acts: no scope an agent can be given carries
// deployment:restore or deployment:open-restored, so neither an agent token
// nor an OAuth delegation ever does, and no instrument (a chat, a step, an
// eval, AppTown's trigger) holds them; a project owner does. Opening is
// not a read: no read role (an Auditor, a viewer) carries it.
func TestNoScopeCarriesRestore(t *testing.T) {
	for _, c := range []access.Capability{access.CapDeploymentRestore, access.CapDeploymentOpenRestored} {
		for _, s := range access.Scopes {
			for _, sc := range s.Caps {
				if sc == c {
					t.Fatalf("the scope %s carries %s", s.Name, c)
				}
			}
		}
		agent := access.Principal{Kind: "user", Name: "alice", Via: "token", Purpose: access.TokenAgent}
		if access.CredentialCovers(agent, c) {
			t.Fatalf("an agent token covers %s", c)
		}
		if access.CredentialCovers(access.Principal{Kind: "user", Name: "alice", Via: "oauth", Scopes: access.ScopeNames()}, c) {
			t.Fatalf("an OAuth delegation with every scope covers %s", c)
		}
		for _, kind := range []string{"console", "run", access.SystemInstrument, "eval"} {
			if access.InstrumentMay(kind, c) {
				t.Fatalf("a %s instrument may %s", kind, c)
			}
		}
		for _, r := range access.ReadCaps {
			if r == c {
				t.Fatalf("%s is among the read capabilities", c)
			}
		}
		owner := false
		for _, oc := range access.OwnerCaps {
			owner = owner || oc == c
		}
		if !owner {
			t.Fatalf("a project owner does not hold %s, as they hold deployment:teardown", c)
		}
	}
}
