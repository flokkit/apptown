// Package verification composes AppTown checks over existing operation owners.
// Conformance owns findings, dispositions and review receipts; the check library
// owns evaluator declarations and classification. This package owns execution
// and comparison policy. Scan observations are retained by Conformance with
// their exact subjects and scanner configuration, independently of Change cleanup. Its check-session artifacts cache evaluator outputs
// and transcripts; Conformance remains the owner of review standing and receipts.
//
// Changes.Check runs checks against a change and caches its source baseline.
// Fleet runs synchronous or queued reviews of current project declarations.
// Scanners prepares images and executes tools; Evaluator interprets shell
// judgments through Effects. CheckJudge runs isolated AI sessions. Runtime
// observes deployments and probes.
// ScanSteps and Guidance accept admitted Workflow executions and order claims,
// artifacts and settlement through Workflow's capabilities. StageScanners reads
// the admission inventory in its caller's transaction. SyncStockScanners binds
// shipped declarations through Integration during bootstrap.
//
// Constructors receive explicit collaborators. No owner holds App or a general
// runtime object. Private execution and comparison helpers stay private; typed
// results cross the change-check and remediation boundaries. Tests of those
// private rules live here, while assembled workflow and transport tests remain
// at their application boundaries.
package verification
