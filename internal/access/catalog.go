// Package access owns who may do what (design/subsystems/
// authorization-capabilities.md): the capability catalog and the stock
// roles as compiled data, the words for scopes and subjects, and, in the
// slices that follow, the decision rule, grants and the directory. AppTown
// keeps the HTTP doors, the receipts' prose and the resource adapters.
package access

import "sort"

// The capability catalog and the stock roles (design/subsystems/authorization-capabilities.md
// §2, §3, §5). Compiled data, not rows: editable capability semantics would let
// an administrator change what an old grant meant. Roles are small named
// bundles; instance-administrator is the explicit root authority.

// Capability is a stable domain verb, resource:verb. It names an act, never an
// HTTP route or a button.
type Capability string

// ScopeKind names a level of the containment hierarchy a grant can sit at.
type ScopeKind string

const (
	ScopeInstance  ScopeKind = "instance"
	ScopeGroup     ScopeKind = "group"
	ScopeProject   ScopeKind = "project"
	ScopeChange    ScopeKind = "change"
	ScopeRun       ScopeKind = "run"
	ScopeDimension ScopeKind = "dimension"
	ScopeTarget    ScopeKind = "release-target"
)

type Definition struct {
	Description string
	// Inherits lists the scope kinds an instance/group grant of this capability
	// reaches DOWN into (§3 table). Empty = a grant applies only at the scope it
	// names and the resources inside that scope's own project/change/run.
	Inherits []ScopeKind
	Risk     string // read | mutate | approve | release | execute | admin
}

// Capability names, grouped as the design lists them.
const (
	CapInstanceReadPublic Capability = "instance:read-public"
	CapInstitutionRead    Capability = "institution:read-structure"

	CapGroupTransferProject Capability = "group:transfer-project"

	CapProjectList          Capability = "project:list"
	CapProjectRead          Capability = "project:read"
	CapProjectCreate        Capability = "project:create"
	CapProjectConfigure     Capability = "project:configure"
	CapProjectDelete        Capability = "project:delete"
	CapProjectManageMembers Capability = "project:manage-members"
	CapProjectImport        Capability = "project:import"

	CapChangeList        Capability = "change:list"
	CapChangeRead        Capability = "change:read"
	CapChangeCreate      Capability = "change:create"
	CapChangeUpdateBrief Capability = "change:update-brief"
	// CapChangeNarrative writes a change's "So far" summary (and a title
	// nobody chose), never the person-written brief. AppTown's own engine
	// holds it, so its automatic refreshes publish.
	CapChangeNarrative     Capability = "change:write-narrative"
	CapChangeWriteRevision Capability = "change:write-revision"
	CapChangeAttach        Capability = "change:attach-evidence"
	CapChangeRunChecks     Capability = "change:run-checks"
	CapChangePreview       Capability = "change:preview"
	CapChangeInvokeLane    Capability = "change:invoke-lane"
	CapChangeSendBack      Capability = "change:send-back"
	CapChangeAbandon       Capability = "change:abandon"
	CapChangeRequestLand   Capability = "change:request-land"
	CapChangeLand          Capability = "change:land"
	CapChangeApprove       Capability = "change:approve"
	CapChangeOverride      Capability = "change:override"

	CapRunRead Capability = "run:read"

	CapRunOperate       Capability = "run:operate"
	CapEvidenceRead     Capability = "evidence:read"
	CapEvidenceAttach   Capability = "evidence:attach"
	CapConformanceRead  Capability = "conformance:read"
	CapConformanceRev   Capability = "conformance:review"
	CapConformanceClear Capability = "conformance:clear-history"
	CapConformanceAI    Capability = "conformance:confirm-ai"
	CapConformanceFix   Capability = "conformance:remediate"

	CapExceptionApprove Capability = "exception:approve"
	CapExceptionRevoke  Capability = "exception:revoke"

	CapProjectWayOfWorking Capability = "project:select-way-of-working"
	CapProjectGuidance     Capability = "project:configure-guidance"
	CapProjectServices     Capability = "project:configure-services"
	CapProjectIntegrations Capability = "project:configure-integrations"
	CapProjectExtensions   Capability = "project:configure-extensions"
	CapProjectModelProfile Capability = "project:configure-model-profile"
	CapProjectWorkflow     Capability = "project:configure-workflow"
	CapReleaseRead         Capability = "release:read"
	CapReleasePromote      Capability = "release:promote"
	CapDeploymentRead      Capability = "deployment:read"
	CapDeploymentOperate   Capability = "deployment:operate"
	// The deployment verbs (design/subsystems/deployment.md).
	CapDeploymentBind     Capability = "deployment:bind-target"
	CapDeploymentDeploy   Capability = "deployment:deploy"
	CapDeploymentTeardown Capability = "deployment:teardown"
	// CapDeploymentRestore fills a deployment's data from a copy, or checks
	// a copy comes back (design/subsystems/deployment.md). It is
	// checked in the copy's project and in the destination's; no scope
	// carries it, so an agent never does.
	CapDeploymentRestore Capability = "deployment:restore"
	// CapDeploymentOpenRestored enters a restored copy of production through
	// its AppTown gate (design/subsystems/deployment.md, "Restored copies"): it reads
	// production rows through the app. It is held with restore but kept
	// apart from it (a person may look without starting places), and it is
	// not a read: no read role, scope or instrument carries it.
	CapDeploymentOpenRestored Capability = "deployment:open-restored"

	CapIdentityUsers  Capability = "identity:manage-users"
	CapIdentityGroups Capability = "identity:manage-groups"
	CapIdentityAuth   Capability = "identity:manage-auth"

	CapAuthzGrants       Capability = "authorization:manage-grants"
	CapStandardsGuidance Capability = "standards:manage-guidance"
	CapStandardsPacks    Capability = "standards:manage-packs"
	CapStandardsPolicies Capability = "standards:manage-policies"
	CapStandardsCatalog  Capability = "standards:manage-catalog"
	// CapStandardsEvals reads evals: how a model or pack version did against
	// what projects run now. A read, so an agent may hold it through its
	// person; starting one stays with standards:manage-packs.
	CapStandardsEvals Capability = "standards:read-evals"
	CapExecWorkflows  Capability = "execution:manage-workflows"

	CapIntegrationConnector Capability = "integration:manage-connectors"
	CapIntegrationServices  Capability = "integration:manage-service-kinds"
	CapIntegrationDeps      Capability = "integration:manage-dependency-kinds"
	CapModelsBindings       Capability = "models:manage-bindings"
	CapModelsSlots          Capability = "models:manage-slots"
	CapPlatformTargets      Capability = "platform:manage-deploy-targets"

	CapPlatformOperate Capability = "platform:operate"
	CapAuditRead       Capability = "audit:read"

	// Secrets: reading declarations and binding
	// status is a read; binding a secret to a store, putting a value into
	// the built-in store and choosing a deployment's profile is one
	// mutate capability. Values are never readable.
	CapSecretRead   Capability = "secret:read"
	CapSecretBind   Capability = "secret:bind"
	CapSecretReveal Capability = "secret:reveal"
)

// readInherits is the §3 table: reads flow from instance/group to the
// projects they own. project:create flows instance -> group. Nothing else.
var readInherits = []ScopeKind{ScopeGroup, ScopeProject}

var Definitions = map[Capability]Definition{
	CapInstanceReadPublic: {"read the explicitly public instance surface", readInherits, "read"},
	CapInstitutionRead:    {"see users, groups, roles and grants", nil, "read"},

	CapGroupTransferProject: {"transfer a project between groups", nil, "admin"},

	CapProjectList:          {"discover visible projects", readInherits, "read"},
	CapProjectRead:          {"read one project's source-facing state", readInherits, "read"},
	CapProjectCreate:        {"create a project for an allowed owning group", []ScopeKind{ScopeGroup}, "mutate"},
	CapProjectConfigure:     {"change ordinary project metadata", nil, "mutate"},
	CapProjectDelete:        {"permanently remove a project", nil, "admin"},
	CapProjectManageMembers: {"change a project's roster and project roles", nil, "admin"},
	CapProjectImport:        {"import an external branch or tree", nil, "mutate"},

	CapChangeList:          {"list changes", readInherits, "read"},
	CapChangeRead:          {"read a change", readInherits, "read"},
	CapChangeCreate:        {"open a change", nil, "mutate"},
	CapChangeUpdateBrief:   {"edit a change's brief", nil, "mutate"},
	CapChangeNarrative:     {"refresh a change's So far summary", nil, "mutate"},
	CapChangeWriteRevision: {"contribute source revisions to a change", nil, "mutate"},
	CapChangeAttach:        {"attach evidence to a change", nil, "mutate"},
	CapChangeRunChecks:     {"run a change's checks", nil, "mutate"},
	CapChangePreview:       {"boot a change's preview", nil, "mutate"},
	CapChangeInvokeLane:    {"start an allowed lane on a change", nil, "mutate"},
	CapChangeSendBack:      {"send a change back to its author", nil, "mutate"},
	CapChangeAbandon:       {"abandon a change", nil, "mutate"},
	CapChangeRequestLand:   {"hand a proposed tip off for landing", nil, "mutate"},
	CapChangeLand:          {"operate a landing attempt: restart, resume, complete an unapproved lane", nil, "mutate"},
	CapChangeApprove:       {"assent at an approval step", nil, "approve"},
	CapChangeOverride:      {"bypass a failed gate with a reason", nil, "approve"},

	CapRunRead: {"read a run", readInherits, "read"},

	CapRunOperate:       {"advance, retry or stop a run", nil, "mutate"},
	CapEvidenceRead:     {"read evidence", readInherits, "read"},
	CapEvidenceAttach:   {"attach evidence to a run", nil, "mutate"},
	CapConformanceRead:  {"read conformance", readInherits, "read"},
	CapConformanceRev:   {"run a read-only conformance review", nil, "mutate"},
	CapConformanceClear: {"clear conformance and remediation history", nil, "admin"},
	CapConformanceAI:    {"confirm one exact AI verdict", nil, "approve"},
	CapConformanceFix:   {"start a remediation", nil, "mutate"},

	CapExceptionApprove: {"approve a conformance exception in a dimension", nil, "approve"},
	CapExceptionRevoke:  {"revoke a conformance exception", nil, "approve"},

	CapProjectWayOfWorking: {"select the project's way of working", nil, "execute"},
	CapProjectGuidance:     {"change what guidance the project adopts", nil, "execute"},
	CapProjectServices:     {"change the project's attached services", nil, "execute"},
	CapProjectIntegrations: {"attach or wire integrations to the project", nil, "execute"},
	CapProjectExtensions:   {"enable extensions on the project", nil, "execute"},
	CapProjectModelProfile: {"choose the project's model profile", nil, "execute"},
	CapProjectWorkflow:     {"select or change the project's workflow copy", nil, "execute"},
	CapReleaseRead:         {"read releases", readInherits, "read"},
	CapReleasePromote:      {"promote a release to a wider tier", nil, "release"},
	CapDeploymentRead:      {"read deployments", readInherits, "read"},
	CapDeploymentOperate:   {"restart or rebuild a deployment, and read its parts' recent logs", nil, "mutate"},
	CapDeploymentBind:      {"point a standing deployment at a target", nil, "mutate"},
	CapDeploymentDeploy:    {"ship a bundle to a standing deployment", nil, "release"},
	CapDeploymentTeardown:  {"remove a deployment's deployment (never its data)", nil, "mutate"},
	CapDeploymentRestore:   {"fill a deployment's data from a copy, or check a copy comes back", nil, "mutate"},
	// Not "read": a read is carried by every scope and instrument, and this
	// one shows production rows.
	CapDeploymentOpenRestored: {"open a restored copy of production data through AppTown gate", nil, "mutate"},

	CapIdentityUsers:  {"manage user accounts", nil, "admin"},
	CapIdentityGroups: {"manage groups", nil, "admin"},
	CapIdentityAuth:   {"manage authentication settings", nil, "admin"},

	CapAuthzGrants:       {"grant and revoke roles and capabilities", nil, "admin"},
	CapStandardsGuidance: {"edit the guidance library", nil, "execute"},
	CapStandardsPacks:    {"edit packs", nil, "execute"},
	CapStandardsPolicies: {"edit policies and the policy library", nil, "execute"},
	CapStandardsCatalog:  {"edit the change catalog", nil, "execute"},
	CapStandardsEvals:    {"read evals of models and pack versions", nil, "read"},
	CapExecWorkflows:     {"edit workflow definitions", nil, "execute"},

	CapIntegrationConnector: {"manage connectors and provider bindings", nil, "admin"},
	CapIntegrationServices:  {"manage service kinds", nil, "execute"},
	CapIntegrationDeps:      {"manage dependency kinds", nil, "execute"},
	CapModelsBindings:       {"manage model provider bindings", nil, "admin"},
	CapModelsSlots:          {"manage model slots", nil, "admin"},
	CapPlatformTargets:      {"manage deploy targets", nil, "admin"},

	CapPlatformOperate: {"operate the platform", nil, "admin"},
	CapAuditRead:       {"read the audit log", readInherits, "read"},

	CapSecretRead:   {"read secret declarations and bindings (names, stores, versions; never values)", readInherits, "read"},
	CapSecretBind:   {"bind a project's secrets to a store, put a value into the built-in store, choose a deployment's profile", nil, "mutate"},
	CapSecretReveal: {"reveal a secret's value held by the built-in store, on the record (administrators only)", nil, "mutate"},
}

// AllCapabilities is the sorted catalog, for the sovereign role and the API.
func AllCapabilities() []Capability {
	out := make([]Capability, 0, len(Definitions))
	for c := range Definitions {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Inherits reports whether a grant at `from` (instance or group) reaches a
// resource whose scope chain includes `to`.
func Inherits(c Capability, to ScopeKind) bool {
	def, ok := Definitions[c]
	if !ok {
		return false
	}
	for _, s := range def.Inherits {
		if s == to {
			return true
		}
	}
	return false
}

// Role is a named capability bundle shipped with AppTown.
type Role struct {
	Name        string
	Title       string // the answerable office, in product words
	Description string
	Scope       ScopeKind // where a grant of this role sits
	Caps        []Capability
	// Surfaced roles appear on the People page and in grant pickers. The
	// rest exist for later profiles.
	Surfaced bool
}

// Stock role names.
const (
	RoleInstanceAdmin     = "instance-administrator"
	RoleStandardsAdmin    = "standards-administrator"
	RoleExecutionAdmin    = "execution-administrator"
	RoleIntegrationAdmin  = "integration-administrator"
	RoleReleaseManager    = "release-manager"
	RoleAuditor           = "auditor"
	RoleProjectCreator    = "project-creator"
	RoleGroupViewer       = "group-viewer"
	RoleGroupMember       = "group-member"
	RoleGroupCreator      = "group-project-creator"
	RoleGroupMaintainer   = "group-maintainer"
	RoleGroupOwner        = "group-owner"
	RoleGroupAuditor      = "group-auditor"
	RoleProjectViewer     = "project-viewer"
	RoleProjectContrib    = "project-contributor"
	RoleProjectMaintainer = "project-maintainer"
	RoleProjectApprover   = "project-approver"
	RoleProjectOwner      = "project-owner"
)

var (
	ReadCaps = []Capability{CapProjectList, CapProjectRead, CapChangeList, CapChangeRead, CapRunRead,
		CapEvidenceRead, CapConformanceRead, CapReleaseRead, CapDeploymentRead, CapSecretRead}
	ContributorCaps = append(append([]Capability{}, ReadCaps...),
		CapChangeCreate, CapChangeUpdateBrief, CapChangeNarrative, CapChangeWriteRevision, CapChangeAttach, CapChangeRunChecks,
		CapChangePreview, CapChangeInvokeLane, CapChangeRequestLand, CapEvidenceAttach,
		CapConformanceRev)
	MaintainerCaps = append(append([]Capability{}, ContributorCaps...),
		CapChangeLand, CapChangeAbandon, CapChangeSendBack, CapRunOperate, CapDeploymentOperate,
		CapProjectServices, CapProjectIntegrations, CapProjectGuidance, CapProjectWorkflow,
		CapProjectWayOfWorking, CapProjectExtensions, CapProjectImport, CapConformanceFix, CapConformanceAI,
		CapProjectManageMembers, CapSecretBind, CapDeploymentDeploy)
	OwnerCaps = append(append([]Capability{}, MaintainerCaps...),
		CapProjectConfigure, CapProjectDelete, CapProjectModelProfile, CapDeploymentBind, CapDeploymentTeardown, CapDeploymentRestore, CapDeploymentOpenRestored)
	// Administration capabilities summarized by the account menu.
	AmenderCaps = []Capability{CapStandardsGuidance, CapStandardsPacks, CapStandardsPolicies, CapStandardsCatalog,
		CapExecWorkflows, CapIntegrationServices, CapIntegrationDeps}
)

var StockRoles = []Role{
	{Name: RoleInstanceAdmin, Title: "Instance administrator", Scope: ScopeInstance, Surfaced: true,
		Description: "The sovereign: holds every capability on this instance.", Caps: AllCapabilities()},
	{Name: RoleStandardsAdmin, Title: "Standards administrator", Scope: ScopeInstance,
		Description: "Manages guidance, packs and policies (the amender).",
		Caps:        []Capability{CapStandardsGuidance, CapStandardsPacks, CapStandardsPolicies, CapStandardsCatalog, CapStandardsEvals}},
	{Name: RoleExecutionAdmin, Title: "Execution administrator", Scope: ScopeInstance,
		Description: "Manages executable workflow definitions.",
		Caps:        []Capability{CapExecWorkflows}},
	{Name: RoleIntegrationAdmin, Title: "Integration administrator", Scope: ScopeInstance,
		Description: "Manages connectors, service and dependency kinds, model bindings.",
		Caps:        []Capability{CapIntegrationConnector, CapIntegrationServices, CapIntegrationDeps, CapModelsBindings, CapModelsSlots}},
	{Name: RoleReleaseManager, Title: "Release manager", Scope: ScopeInstance, Surfaced: false,
		Description: "Promotes releases to wider tiers (the releaser).",
		Caps:        []Capability{CapReleaseRead, CapReleasePromote, CapDeploymentRead, CapDeploymentDeploy}},
	{Name: RoleAuditor, Title: "Auditor", Scope: ScopeInstance,
		Description: "Reads everything, changes nothing.",
		Caps:        append(append([]Capability{}, ReadCaps...), CapInstitutionRead, CapAuditRead, CapStandardsEvals)},
	{Name: RoleProjectCreator, Title: "Project creator", Scope: ScopeInstance, Surfaced: true,
		Description: "Creates projects through approved creation profiles.",
		Caps:        []Capability{CapProjectCreate, CapProjectList}},

	{Name: RoleGroupViewer, Title: "Group viewer", Scope: ScopeGroup,
		Description: "Discovers projects visible through this group.", Caps: []Capability{CapProjectList}},
	{Name: RoleGroupMember, Title: "Group member", Scope: ScopeGroup, Surfaced: true,
		Description: "Belongs to the group. Carries no project capability by itself.", Caps: []Capability{}},
	{Name: RoleGroupCreator, Title: "Group project creator", Scope: ScopeGroup,
		Description: "Creates projects owned by the group.", Caps: []Capability{CapProjectCreate}},
	{Name: RoleGroupMaintainer, Title: "Group maintainer", Scope: ScopeGroup,
		Description: "Reserved group office; currently carries no operation permissions.",
		Caps:        []Capability{}},
	{Name: RoleGroupOwner, Title: "Group owner", Scope: ScopeGroup, Surfaced: true,
		Description: "Creates and transfers projects for this group.",
		Caps:        []Capability{CapGroupTransferProject, CapProjectCreate}},
	{Name: RoleGroupAuditor, Title: "Group auditor", Scope: ScopeGroup,
		Description: "Reads projects and evidence visible through this group.",
		Caps:        append(append([]Capability{}, ReadCaps...), CapAuditRead)},

	{Name: RoleProjectViewer, Title: "Project viewer", Scope: ScopeProject, Surfaced: true,
		Description: "Reads the project, its changes, runs, evidence and deployments.", Caps: ReadCaps},
	{Name: RoleProjectContrib, Title: "Project contributor", Scope: ScopeProject, Surfaced: true,
		Description: "Opens changes, contributes revisions, runs checks, previews, hands off for landing.",
		Caps:        ContributorCaps},
	{Name: RoleProjectMaintainer, Title: "Project maintainer", Scope: ScopeProject, Surfaced: true,
		Description: "Contributor plus landing, abandoning, operating runs and deployments, and project configuration.",
		Caps:        MaintainerCaps},
	{Name: RoleProjectApprover, Title: "Project approver", Scope: ScopeProject, Surfaced: true,
		Description: "Approves at approval steps that name them. Need not write code or land.",
		Caps:        append(append([]Capability{}, ReadCaps...), CapChangeApprove, CapChangeOverride, CapExceptionApprove, CapExceptionRevoke)},
	{Name: RoleProjectOwner, Title: "Project owner", Scope: ScopeProject, Surfaced: true,
		Description: "Maintainer plus membership, configuration and deletion authority.",
		Caps:        OwnerCaps},
}

func RoleByName(name string) *Role {
	for i := range StockRoles {
		if StockRoles[i].Name == name {
			return &StockRoles[i]
		}
	}
	return nil
}

// Holds reports whether a role bundles a capability.
func (rv *Role) Holds(c Capability) bool {
	for _, x := range rv.Caps {
		if x == c {
			return true
		}
	}
	return false
}
