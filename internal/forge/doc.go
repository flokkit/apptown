// Package forge assembles the application and its transports.
//
// Start with app.go for construction and route_table.go for HTTP bindings,
// guards and capabilities. HTTP handler groups live in httpapi; MCP tools live
// in mcpapi. Follow the bound method to its operation owner. Those packages never
// import this package or receive App through an interface or callback bundle.
//
// Workflow owns admission, claims, decisions and atomic settlement. Workcommands
// prepares frozen starts and composes landing, remediation and durable follow-up
// effects. Workflowexec orders external work for agents, reviews, scripts, tests,
// deployment, previews, repository work and nested workflow calls. Each runner
// owns its ordering; there is no universal runner with optional collaborators.
//
// Agentenv prepares shared workspace capabilities. Agentharness/pi owns Pi's
// configuration and output interpretation. Conversation composes chat admission
// and briefing; console/pi retains long-lived session processes. Inference owns
// current workload authority, private provider access, admission and streaming
// accounting. Harness configuration receives runtime tokens, never provider keys.
//
// Projectlifecycle composes project/adoption commands; Verification composes
// checks over Conformance; Maintenance owns periodic resource cleanup. Jobs owns
// durable scheduling and delivery, not these application policies. Transaction
// participants still join their caller's transaction before any external effects.
//
// Root retains construction, instance boot/recovery/shutdown, HTTP identity,
// OAuth, Git HTTP, JSON-RPC framing, terminal websockets and host routing. Small
// cross-owner transaction adapters and named narrative/console composition remain
// explicit here. README.md maps the call sequences and retained responsibilities.
//
// Immutable settings live in configuration. Repository locks, capacity, progress,
// runtime tokens and inventory have separate lifetimes. The root coordinates
// bounded shutdown; stopping the daemon does not end long-lived console sessions.
package forge
