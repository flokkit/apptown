// Package access is the authorization rule and its vocabulary: who is asking
// (principal, caller), what they want (capability, resource, permission), the
// catalog of capabilities and stock roles, the decision, and the denial. It
// owns no tables, reads no database, and imports nothing of forge, so a
// decision is testable on its own.
package access

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/stamp"
)

// The authorization decision (authorization-capabilities.md §12). One rule
// for every protocol: HTTP guards, the MCP server, the CLI and workers all
// call Decide and translate its one answer. Root is explicit; other roles
// are bundles the catalog expands. The rule reads what it
// needs through Facts, so a decision is testable with no database and the
// reads happen exactly when the rule asks (grants only after an instrument
// has been resolved to its person).
//
// Reason codes are stable so a client can act on them; the prose is for
// the person.
const (
	DenyMissingCapability = "missing_capability"
	DenyOutsideScope      = "outside_scope"
	DenyDelegationNarrow  = "delegation_too_narrow"
	DenyResourceState     = "resource_state_conflict"
	DenySegregation       = "segregation_of_duties"
	DenyQuorum            = "quorum_incomplete"
	DenyReauth            = "reauthentication_required"
	DenyExpiredObjectCap  = "expired_object_capability"
	DenyAccountDisabled   = "account_disabled"
	DenyNotEligible       = "not_an_approver"
)

// Principal is who is present, resolved from the authenticated identity.
// Instruments (a console session, a step agent) are not people: they carry
// the person behind them when there is one and are bounded by §11's tables.
type Principal struct {
	// ID is the principal who answers: a person's UUID for a user, the
	// person's for a console they wield; Nil when none was resolved (an
	// anonymous caller, a run, a name read from a header before its account).
	ID         ids.ID
	Kind       string // user | anonymous | instrument
	Name       string // username, "anonymous", or the instrument's own name
	Person     string // for a console: the person wielding it; "" for a run
	Instrument string // "" | console | run | forge
	Via        string
	ProjectID  ids.ID // the one project an instrument may act in
	ConsoleID  string
	Role       string // token administrator ceiling; otherwise a display projection, never authority
	// Purpose is a personal token's, chosen when it was made: TokenCLI is
	// its person's own command line, TokenAgent a coding agent's
	// connection. "" for every other credential.
	Purpose string
	// An OAuth delegation behind the request: the rule narrows to its
	// scopes. Zero for sessions and personal tokens (full breadth).
	DelegationID ids.ID
	Scopes       []string
	// Credential is a non-secret reference to the credential behind the
	// request (token:<name>, session:<id>, oauth:<delegation id>,
	// header:<user>, system:<binding>), so a long operation can ask later
	// whether that credential is still live. "" when none was resolved.
	Credential string
}

// Channel bounds what the channel may exercise of the person's authority.
// Sessions and legacy personal tokens are "full breadth" (§1): no narrowing
// beyond current grants.
type Channel struct {
	Kind string // full-breadth | legacy-token | instrument | anonymous | oauth
	ID   string
}

// DelegationOf is the channel's bound, from the principal's provenance.
func DelegationOf(pr Principal) Channel {
	switch {
	case pr.Kind == "instrument":
		return Channel{Kind: "instrument", ID: pr.Name}
	case pr.Kind == "anonymous":
		return Channel{Kind: "anonymous"}
	case pr.Via == "oauth":
		return Channel{Kind: "oauth", ID: pr.DelegationID.String()}
	case AgentToken(pr):
		return Channel{Kind: "agent-token"}
	case pr.Via == "token":
		return Channel{Kind: "legacy-token"}
	}
	return Channel{Kind: "full-breadth"}
}

// Resource is the thing acted on, with its containing scopes resolved.
type Resource struct {
	ProjectName string // display name supplied by the resource owner, never an authority fact
	Type        string // instance | group | project | change | run | step | deployment | dimension | release-target
	ID          string
	ProjectID   ids.ID
	Group       string // the project's owning group
	ChangeID    ids.ID
	RunID       ids.ID
}

// Context describes the decision receipt.
type Context struct {
	Consequential bool   // write a decision receipt (protected acts: yes)
	Route         string // for the receipt's context
}

// Decision is the one answer.
type Decision struct {
	Allow     bool     `json:"allow"`
	Code      string   `json:"code,omitempty"`
	Reason    string   `json:"reason"`
	GrantIDs  []ids.ID `json:"grantIds,omitzero"`
	Roles     []string `json:"roles,omitempty"`
	Paths     []string `json:"paths,omitempty"` // membership paths that mattered, e.g. group:approvers
	ReceiptID ids.ID   `json:"receiptId,omitzero"`
}

// Grant is a grant row as the rule reads it.
type Grant struct {
	ID          ids.ID
	SubjectType string
	Subject     string
	RoleName    string
	Capability  Capability
	ScopeType   ScopeKind
	ScopeID     string
	Origin      string
	GrantedBy   string
	// GrantedByPrincipal is the granter's principal; zero for AppTown, or
	// a granter not resolved to a person.
	GrantedByPrincipal ids.ID
	Reason             string
	CreatedAt          string
	ExpiresAt          stamp.Time
	ReviewAt           stamp.Time
	RevokedAt          stamp.Time
}

// Facts is what the rule reads while deciding. Each read happens only when
// the rule reaches it.
type Facts interface {
	// Disabled reports whether a user may not act as an account: it is
	// disabled, or there is no such account. A principal that was admitted
	// is judged by its ID (its name may have changed or gone to someone
	// else); a name alone, by the account of that name. Unknown is refused,
	// never enabled.
	Disabled(pr Principal) bool
	// Grants is the principal's active grants: their own, their groups' and
	// everyone's, in one snapshot.
	Grants(pr Principal) []Grant
}

// instrumentCaps is §11's table: what a console, a step agent or one of the
// forge's own triggers may do at all, before the person's own authority is
// consulted. Reads are implied. "forge" is the scheduler and the webhook
// trigger acting for AppTown itself, and the engine advancing a run whose
// stage finished: the interim §11 names until worker identities exist.
// Open a change, start its run, advance it, in one project.
var instrumentCaps = map[string][]Capability{
	"console": {CapChangeInvokeLane, CapChangePreview, CapChangeRequestLand, CapChangeRunChecks, CapProjectRead,
		CapChangeCreate, CapChangeWriteRevision, CapChangeAttach},
	"run": {CapChangeInvokeLane, CapChangePreview, CapChangeRunChecks, CapProjectRead, CapEvidenceAttach,
		CapChangeWriteRevision},
	"forge": {CapChangeCreate, CapChangeInvokeLane, CapRunOperate, CapProjectRead, CapChangeNarrative},
	// An eval's driver in one of its throwaway copies, bound to that copy:
	// open the arm's Change, run it and land it under the eval's authority
	// (which the landing guard judges against the copy record), take its
	// deployment down, abandon it.
	"eval": {CapChangeCreate, CapChangeInvokeLane, CapChangeRequestLand, CapRunOperate, CapProjectRead, CapChangeNarrative,
		CapChangeAbandon, CapDeploymentTeardown},
}

// InstrumentMay is §11's table as a question.
func InstrumentMay(kind string, c Capability) bool {
	if Definitions[c].Risk == "read" {
		return true
	}
	return containsCap(instrumentCaps[kind], c)
}

// Decide evaluates §12's order and returns one decision. It writes nothing;
// the caller records the receipt.
func Decide(pr Principal, d Channel, c Capability, res Resource, ctx Context, facts Facts) Decision {
	def, known := Definitions[c]
	if !known {
		return Decision{Code: DenyMissingCapability, Reason: fmt.Sprintf("%s is not a capability this AppTown knows", c)}
	}
	// 1. A disabled account, or a name with no account, fails before
	// anything else.
	if pr.Kind == "user" && facts.Disabled(pr) {
		return Decision{Code: DenyAccountDisabled, Reason: fmt.Sprintf("%s's account is disabled or does not exist", pr.Name)}
	}
	// 2. The delegation's bound: an OAuth client may exercise only the
	// scopes the person consented to (§9, §10), an agent token only the
	// scope catalogue. Scopes only subtract.
	if !CredentialCovers(pr, c) {
		return Decision{Code: DenyDelegationNarrow, Reason: CredentialNarrowReason(pr, def.Description)}
	}
	// 2. Instruments: bounded by §11's table and their one project. A console
	// then also needs its person to hold the capability; a run token or the
	// forge's own trigger has no person and holds exactly the table.
	if pr.Kind == "instrument" {
		if !InstrumentMay(pr.Instrument, c) {
			return Decision{Code: DenyDelegationNarrow,
				Reason: fmt.Sprintf("a %s session may not %s; that needs a person", pr.Instrument, def.Description)}
		}
		if !res.ProjectID.IsZero() && !pr.ProjectID.IsZero() && res.ProjectID != pr.ProjectID {
			return Decision{Code: DenyDelegationNarrow, Reason: "this session works on another project"}
		}
		// A step agent has no person whose current grants could bound its
		// reads, so they are implied only inside its project: on a record
		// of the whole instance (the ledger, people, grants) it holds
		// exactly its table. Otherwise revoking the shared read grant would
		// stop a person and their chat but not a run they start.
		if pr.Instrument == "run" && res.ProjectID.IsZero() && !containsCap(instrumentCaps["run"], c) {
			return Decision{Code: DenyDelegationNarrow,
				Reason: fmt.Sprintf("a step agent reads its own project; to %s needs a person", def.Description)}
		}
		if pr.Instrument == "run" {
			return Decision{Allow: true, Reason: "a step agent, within its run's table"}
		}
		if pr.Instrument == "eval" {
			if pr.ProjectID.IsZero() {
				return Decision{Code: DenyDelegationNarrow, Reason: "an eval acts only in one of its copies"}
			}
			return Decision{Allow: true, Reason: "an eval's driver in one of its copies, within its table"}
		}
		if pr.Instrument == "forge" {
			return Decision{Allow: true, Reason: "AppTown's own trigger, within its table"}
		}
		pr = Principal{ID: pr.ID, Kind: "user", Name: pr.Person, Via: pr.Via, Role: pr.Role}
	}
	// 3–6. Grants at an applicable scope, through role expansion.
	grants := facts.Grants(pr)
	// Root reaches every project; credentials and instruments narrow it first.
	if pr.Kind == "user" {
		if g, root := RootGrant(grants); root && (pr.Via != "token" || pr.Role == "admin") {
			return Decision{Allow: true, GrantIDs: []ids.ID{g.ID}, Roles: []string{g.RoleName},
				Reason: fmt.Sprintf("%s may %s: Instance administrator", pr.Name, def.Description)}
		}
	}
	if rootOnly(c) {
		return Decision{Code: DenyMissingCapability, Reason: "instance administrator authority is required"}
	}
	var holders []Grant // grants that carry c at all
	var applicable []Grant
	for _, g := range grants {
		if g.RoleName == RoleInstanceAdmin || !GrantHolds(g, c) {
			continue
		}
		holders = append(holders, g)
		if g.Applies(c, res) {
			applicable = append(applicable, g)
		}
	}
	if len(applicable) == 0 {
		prose := denialProse(pr, c, res)
		if len(holders) == 0 {
			return Decision{Code: DenyMissingCapability, Reason: prose}
		}
		return Decision{Code: DenyOutsideScope, Reason: prose + " (you hold it elsewhere, not here)"}
	}
	dec := Decision{Allow: true}
	seenRole := map[string]bool{}
	for _, g := range applicable {
		dec.GrantIDs = append(dec.GrantIDs, g.ID)
		if g.RoleName != "" && !seenRole[g.RoleName] {
			seenRole[g.RoleName] = true
			dec.Roles = append(dec.Roles, g.RoleName)
		}
		if g.SubjectType == "group" && g.Subject != pr.Name {
			dec.Paths = append(dec.Paths, "group:"+g.Subject)
		}
	}
	dec.Reason = allowProse(pr, c, applicable)
	return dec
}

// GrantHolds reports whether a grant carries a capability, directly or
// through its role.
func GrantHolds(g Grant, c Capability) bool {
	if g.Capability != "" {
		return g.Capability == c
	}
	rv := RoleByName(g.RoleName)
	return rv != nil && rv.Holds(c)
}

// GrantApplies is §3: a grant sits at a scope and covers the resources
// inside it; instance and group grants reach down into projects only for
// the capabilities the catalog marks inheritable.
func GrantApplies(g Grant, c Capability, res Resource) bool {
	switch g.ScopeType {
	case ScopeInstance:
		switch res.Type {
		case "instance", "group":
			return true // instance-level acts, and group acts, are instance-scoped resources
		case "dimension":
			return res.ProjectID.IsZero() || Inherits(c, ScopeProject)
		case "release-target":
			return Inherits(c, ScopeProject)
		}
		return Inherits(c, ScopeProject)
	case ScopeGroup:
		if res.Type == "group" {
			return res.ID == g.ScopeID
		}
		return res.Group != "" && res.Group == g.ScopeID && Inherits(c, ScopeProject)
	case ScopeProject:
		return !res.ProjectID.IsZero() && res.ProjectID.String() == g.ScopeID
	case ScopeChange:
		return !res.ChangeID.IsZero() && res.ChangeID.String() == g.ScopeID
	case ScopeRun:
		return !res.RunID.IsZero() && res.RunID.String() == g.ScopeID
	case ScopeDimension:
		return res.Type == "dimension" && res.ID == g.ScopeID
	case ScopeTarget:
		return res.Type == "release-target" && (g.ScopeID == "" || g.ScopeID == "*" || res.ID == g.ScopeID)
	}
	return false
}

func containsCap(xs []Capability, c Capability) bool {
	for _, x := range xs {
		if x == c {
			return true
		}
	}
	return false
}

// --- prose ----------------------------------------------------------------------

// A refusal never names the project: the caller may not be able to read it,
// and guessing an address must not reveal what it is called.
func denialProse(pr Principal, c Capability, res Resource) string {
	def := Definitions[c]
	where := "on this AppTown"
	if !res.ProjectID.IsZero() {
		where = "on this project"
	}
	who := pr.Name
	if pr.Kind == "anonymous" {
		who = "Someone who is not signed in"
	}
	roles := RolesCarrying(c, res)
	fix := "an administrator can grant it on the People page"
	switch {
	case containsCap(AmenderCaps, c):
		// Until the split administrators are surfaced, this is what the
		// People page calls it.
		fix = "that comes with the amend permission; an administrator can grant it on the People page"
	case c == CapReleasePromote:
		fix = "that comes with the release permission; an administrator can grant it on the People page"
	case len(roles) > 0:
		fix = "that comes with " + joinOr(roles) + "; an administrator can grant it on the People page"
	}
	return fmt.Sprintf("%s may not %s %s — %s.", who, def.Description, where, fix)
}

func allowProse(pr Principal, c Capability, via []Grant) string {
	g := via[0]
	what := "a direct grant"
	if g.RoleName != "" {
		if rv := RoleByName(g.RoleName); rv != nil {
			what = rv.Title
		}
	}
	through := ""
	if g.SubjectType == "group" {
		through = " through group " + g.Subject
	}
	return fmt.Sprintf("%s may %s: %s%s", pr.Name, Definitions[c].Description, what, through)
}

// RolesCarrying names the surfaced roles that bundle c at the resource's
// natural scope, for the "how to get it" half of a denial.
func RolesCarrying(c Capability, res Resource) []string {
	want := ScopeProject
	if res.Type == "instance" || res.Type == "group" {
		want = ScopeInstance
	}
	var out []string
	for _, rv := range StockRoles {
		if rv.Surfaced && rv.Scope == want && rv.Holds(c) {
			out = append(out, rv.Title)
		}
	}
	return out
}

func joinOr(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}

// --- OAuth consent scopes -----------------------------------------------------

// Scope is one consent word (mcp.md §4) mapped onto capabilities. A scope
// names what a client may ask for; the rule still evaluates the person's
// live grants underneath.
type Scope struct {
	Name  string
	Means string // product language for the consent screen
	Caps  []Capability
}

var Scopes = []Scope{
	{"apptown:user:read", "read your projects, Changes, checks, evidence and runs", ReadCaps},
	{"apptown:change:create", "open Changes in your name", []Capability{CapChangeCreate, CapChangeUpdateBrief, CapChangeNarrative}},
	{"apptown:change:write", "push revisions to Changes you may write", []Capability{CapChangeWriteRevision}},
	{"apptown:change:check", "run a Change's checks", []Capability{CapChangeRunChecks}},
	{"apptown:change:preview", "boot a Change's preview", []Capability{CapChangePreview}},
	{"apptown:change:request-land", "hand a Change off for approval", []Capability{CapChangeRequestLand}},
	{"apptown:evidence:attach", "attach evidence to a Change", []Capability{CapChangeAttach, CapEvidenceAttach}},
	// The rest of what the MCP door offers an agent. Approving, landing and
	// administration are in no scope: an agent never inherits them.
	{"apptown:project:create", "create projects in your name", []Capability{CapProjectCreate}},
	{"apptown:change:run", "put AppTown's own agent to work on a Change", []Capability{CapChangeInvokeLane}},
	{"apptown:change:return", "send a Change back with a note, or abandon it", []Capability{CapChangeSendBack, CapChangeAbandon}},
}

func ScopeNames() []string {
	out := make([]string, 0, len(Scopes))
	for _, s := range Scopes {
		out = append(out, s.Name)
	}
	return out
}

func ScopeMeans(name string) string {
	for _, s := range Scopes {
		if s.Name == name {
			return s.Means
		}
	}
	return ""
}

// Personal token purposes. A token without one is refused when presented:
// tokens made before purposes existed are classified by hand, never
// defaulted.
const (
	TokenCLI   = "cli"
	TokenAgent = "agent"
)

// ValidTokenPurpose reports whether p is a purpose a token may be made with.
func ValidTokenPurpose(p string) bool { return p == TokenCLI || p == TokenAgent }

// CredentialCovers is the agent bound every authority check applies before
// grants. An agent's credential, an OAuth delegation or an agent-purpose
// personal token, reaches only the scope catalogue: an OAuth delegation its
// consented scopes, an agent token every scope. No scope covers approving,
// landing or administration, so an agent never inherits those from its
// person. Every other credential is not narrowed here.
func CredentialCovers(pr Principal, c Capability) bool {
	switch {
	case pr.Via == "oauth":
		return ScopesCover(pr.Scopes, c)
	case AgentToken(pr):
		return ScopesCover(ScopeNames(), c)
	}
	return true
}

// AgentToken reports an agent-purpose personal token.
func AgentToken(pr Principal) bool { return pr.Via == "token" && pr.Purpose == TokenAgent }

// AgentCredential reports a coding agent's credential: an OAuth delegation
// or an agent token. An act a person may do for themselves without any
// capability (disconnecting their own connections) still needs the person.
func AgentCredential(pr Principal) bool { return pr.Via == "oauth" || AgentToken(pr) }

// CredentialNarrowReason says why CredentialCovers refused.
func CredentialNarrowReason(pr Principal, does string) string {
	if pr.Via == "token" {
		return fmt.Sprintf("an agent token may not %s; that needs %s's own sign-in or command line", does, pr.Name)
	}
	return fmt.Sprintf("the connected agent was not allowed to %s; %s can reconnect it with that permission", does, pr.Name)
}

// ScopesCover: does a delegation's scope list reach a capability? Reads
// are covered by any scope (a client that may write may look).
func ScopesCover(scopes []string, c Capability) bool {
	if Definitions[c].Risk == "read" && len(scopes) > 0 {
		return true
	}
	for _, name := range scopes {
		for _, s := range Scopes {
			if s.Name == name && containsCap(s.Caps, c) {
				return true
			}
		}
	}
	return false
}
