// Package workflow owns Changes, Runs, Steps and their database-backed lifecycle.
//
// Start with service.go for construction and README.md for the package map
// and a complete retry call path. Commands live beside their implementation;
// runtime.go contains the daemon's dispatch and shutdown controls.
//
// Service is the verified-caller command/query boundary. Its authority binds
// current permission, receipt attribution and inference ownership to stored
// project scope. Already-admitted execution owners do not establish user access.
//
// Run admission orders bounded preparation, readiness and dispatch. Execution
// issues private attempt claims. Decisions, retries and rework own their guarded
// transitions and receipts. Advancement, assertions, landing and merge owners
// settle stages with prepared-source/revision fences. dispatcher
// owns concurrent stage execution and synchronous shutdown admission closure.
//
// Stores and SQLC queries stay private. Named transaction participants allow
// Deployment, jobs, policy and adoption owners to contribute their own receipts or
// observations without giving Work a general repository or an App dependency.
// External execution happens outside SQL transactions. Explicit reruns and the
// existing interrupted-work policy remain; no additional recovery loop or chat
// credential expiry is introduced here.
//
// See design/architecture/work-authority.md for the caller/execution distinction,
// invariants, and the remaining consumers outside this package's current boundary.
package workflow
