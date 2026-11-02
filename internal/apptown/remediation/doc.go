// Package remediation is the queue that fixes findings: one governed Change
// per project at a time, worked by a declared pool of workers, verified by a
// review, with "stuck" as a named state a person sees.
//
// It is a facade (design/architecture/domains.md, principle 6): it composes
// Work and Conformance, owns the transaction, and owns four tables of its own
// — remediations, remediation_items, remediation_attempts and worker_pools —
// together with the pass that moves them, run as the job queue's
// remediation-pass job (PassJob). It is also a Work user: each
// item is a Change on the project's quick-change or implement lane.
//
// What it does not own arrives in Dependencies: Work's doors under AppTown
// principal, the conformance lens, the resolver that turns an item into a
// start definition, the post-landing review, and whether a person is busy on
// the project. Queued item reads explain their current hold using the same
// occupancy decision as scheduling and locked attempt admission. They identify
// blocking Changes and remediations for links; job summaries count waiting
// reasons separately from recorded item states. Retry feedback stays recorded
// while the current waiting explanation is computed on read. The design is design/subsystems/remediation.md.
package remediation
