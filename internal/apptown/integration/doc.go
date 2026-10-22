// Package integration owns provider bindings, service-kind definitions and project
// attachments. [Service] assembles those stores and their environment rendering.
// Callers authorize configuration changes and redact private configuration before
// returning it through an API.
//
// # Bindings and attachments
//
// A [Binding] names a provider of a capability and its versioned configuration.
// Managed stock/runtime bindings have a separate editing policy from user bindings.
// An attachment records which binding and resource a project uses; configuring an
// instance binding does not attach it to every project.
//
// [Attachments.Attach] commits a project attachment and its audit receipt together.
// [Attachments.Remember] stores engine-private resources, which do not count as
// project attachments. [HasCapabilitiesIn] checks actual attachments and matching
// bindings within the caller's transaction. [Attachments.Detach] removes the record
// without deprovisioning the provider resource.
//
// # Service kinds and contributions
//
// [ParseDefinition] validates a YAML service kind: connector/resource fields,
// environment templates, steering and guidance. Reserved compiled capabilities
// cannot be replaced. Direct resources use supplied data; provisioning a linked
// resource still requires a compiled provider adapter.
//
// [Service.Environment] renders attached service templates and compiled error and
// feedback contributions for application containers. Templates can refer to
// connector, resource and project fields, or to a named secret. Rendering secrets
// places plaintext in the returned environment. Unresolved templates are omitted;
// convenience reads can suppress errors, so an empty environment is not proof that
// all required configuration was read successfully.
//
// Provider execution, model credentials and workflow admission stay with their
// respective owners. A repository-wiring Change is admitted by Work; [LinkWireIn]
// joins that admission transaction to record the Change on the attachment, so the
// work and its link commit together or not at all, and a live wiring Change blocks
// a second one. There is no universal contribution object that grants
// every runtime access to every integration.
package integration
