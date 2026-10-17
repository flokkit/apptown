// Package mcpapi presents Forge operations as MCP tools. Each tool group binds
// only its collaborators; calls carry a verified caller and response-link base.
// Each binding constructs an owner-authorized, caller-filtered or public command
// with its implementation named. Assembly rejects missing policies and duplicate
// tools. Run is the invocation boundary; no raw exported function bypasses it.
// Exceptions have executable positive/negative evidence in Forge's policy suite.
// Authentication, session negotiation and JSON-RPC framing stay in forge.
package mcpapi
