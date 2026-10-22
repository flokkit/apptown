# MCP interface

The user MCP endpoint is `/mcp/user`. The official MCP Go SDK
(`github.com/modelcontextprotocol/go-sdk`) handles JSON-RPC framing, the
streamable HTTP transport, protocol-version negotiation and sessions. Root AppTown
decides which credential opens the door, whom a call acts as, the audit of each
session and how tool outcomes are reported. `mcpapi` defines the tool catalog,
inputs, responses and application calls. Tools receive authenticated context,
not App.

Audience-bound OAuth access tokens and agent-purpose personal tokens
authenticate this endpoint. A request here is always an agent's: a `cli`
personal token, which acts with its person's full authority, is refused with
a 401 that names the agent-token alternative. An agent token keeps its bound on
`/api/` too, so it cannot approve, land or administer there either.
The OAuth scope catalogue (`access.Scopes`) is that bound. It covers every act
this door offers an agent, and no scope covers approving, landing or
administration.
Browser cookies and an arbitrary actor header do not substitute for an MCP
credential. Credential restrictions and current authority apply to application
operations; a tool name is not a permission grant.

Every tool declares an owner-checked, filtered-read or public contract. Catalog
validation rejects missing policy declarations. Tool implementations call the
same application owners used by other transports. OAuth/instrument restrictions
still limit which operations are available.

A session belongs to the user whose credential opened it; a request carrying
someone else's session id is refused. Each session is audited once, naming the
client. DELETE ends a session. Session state is in memory and idle sessions
close after a day; after a restart a client's session is unknown and it
initializes again, as the transport specifies. Sessions are distinct from a
persisted AppTown console conversation. The SDK's loopback Host-header guard is
off because apptownd runs behind a reverse proxy and this door admits only bearer
credentials.

An external coding agent uses its own runtime. Connecting over MCP does not
provision a container, private Docker or the agent's model service. Follow the
[connection guide](../../stock/docs/guides/connect-a-coding-agent.md) for client
setup.

## Implementation references

- [internal/apptown/mcp_server.go](../../internal/apptown/mcp_server.go)
- [internal/apptown/mcpapi/catalog.go](../../internal/apptown/mcpapi/catalog.go)
- [internal/apptown/mcpapi/doc.go](../../internal/apptown/mcpapi/doc.go)
- [internal/apptown/oauth.go](../../internal/apptown/oauth.go)
