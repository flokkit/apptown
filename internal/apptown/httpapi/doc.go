// Package httpapi translates HTTP requests into domain operations.
//
// Follow a request from internal/apptown/route_table.go to the named group's method, then
// to its domain operation. Each group declares its own collaborators; none
// retains App. A group may span files when its request and response logic belong
// together. Construction belongs in *_wiring.go or the route table.
//
// Change and run surfaces are in changes.go, runs*.go, traces.go and
// workspaces.go. Attachments use attachments.go and untrusted_file.go.
// Project creation, deletion, repository setup and pack setup call projectlifecycle.
// Project services call repositoryedit; Change admission calls changeactions.
// Console messages and uploads use console_messages.go; complaint intake uses
// conversation scope. Console admission/end use narrow operation ports;
// console_* files keep session-token scope separate from ordinary user requests.
// Project/Change overview and release-history files own their private views.
// Model listing and discovery call the inference catalog, which the gateway admits by.
// Guidance files consume shared agent-steering observations.
// Adoption, pack, policy, workflow and catalog files expose their respective
// owners. Configuration and compose files validate and render native contracts.
// Secret, account, authorization and member files expose management operations.
// ADR, conformance, remediation, usage, timing and audit files expose records
// and commands from their named domains.
//
// Provider configuration and project attachment commands use connectors.go,
// integration_listing.go and project_attachments.go, backed by Integration.
// release_operations.go and backup files call Deployment's provider operations;
// deployment_listing.go, infrastructure.go and tiers.go project runtime state.
// repository_views.go delegates mutations to repositoryedit.Integrity and
// obtains branch diffs through SCM. findings.go uses Conformance's evidence
// overlay and disposition matching. workflow_drafts.go calls a single bounded
// drafting operation; it never receives inference keys or the App runtime.
//
// request.go and change_request.go share resource lookup and authorization
// translation. json_command.go decodes a bounded, complete JSON object before
// invoking a typed command continuation. Keep validation and effects inside
// that continuation; malformed input must never reach the owner. http.go owns
// response encoding and parameter parsing; *_errors.go preserves
// domain-specific error mapping. Private response shapes stay beside handlers.
//
// Authentication and policy-bearing route constructors remain in internal/apptown. A
// required policy always enforces its capability; owner/filtered exceptions name
// their implementation and have executable contracts in policy_evidence_test.go.
// Aggregate pages compose authorized Project, Work, ADR and Adoption reads.
// Each owner obtains capability-specific project visibility from Identity;
// transports never turn an ID slice into authority. Fleet uses one bulk
// Adoption observation alongside shared pack metadata. Visibility enumeration
// and caller-filtered route admission do not record authorization receipts.
// Atomic settings edits call projectlifecycle.UpdateSettings or
// wow.EditBlock once, rather than coordinating independent persistence commands.
// Groups consume
// the access.Caller installed by assembly; attribution headers are not identity.
package httpapi
