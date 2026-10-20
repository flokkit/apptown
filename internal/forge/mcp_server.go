package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/mcpapi"
	"flokkit.io/forge/internal/ids"
)

// The user MCP server (design/subsystems/mcp.md §2 "User MCP"): a coding agent
// on someone's desktop points at https://<forge>/mcp/user and works as that
// person, within the tool catalog in mcpapi.
//
// The protocol itself (JSON-RPC, the streamable HTTP transport, sessions,
// protocol-version negotiation) is the official Go SDK's. What stays here is
// AppTown's part: which credential opens the door, who a call acts as, the
// audit of each session, and how a tool's outcome is reported.
//
// Why this lives outside /api/: AuthMiddleware only runs on /api/ paths, and
// the MCP door must not inherit the browser's doors. See mcpIdentity.

const (
	mcpPath          = "/mcp/user"
	mcpMaxBody       = 1 << 20 // one request is a tool call, never a file
	mcpServerVersion = "dev"   // the binary carries no build version yet
	// Sessions live in this process's memory. An idle one is closed after a
	// day; a restart forgets them all and the client initializes again, as
	// the streamable HTTP transport specifies for an unknown session.
	mcpSessionIdle = 24 * time.Hour
)

// mcpRequest is what the door established for one HTTP request. It reaches a
// tool through the SDK's token info, because the SDK runs a session's calls
// outside the request's own context.
type mcpRequest struct {
	id      *authIdentity
	baseURL string
}

type mcpRequestKey struct{}

// mcpDoor is the user MCP endpoint, one per App so sessions outlive the
// HTTP handler the route table rebuilds. Registering the tools makes the SDK
// convert and validate every tool's schema, about 1.8 MB of allocation per
// App. Production builds one App, but the tests build about a thousand per
// run, and building the server with the App was a third of the root tests'
// allocation. So the SDK server is built on the door's first admitted
// request: an App that is never asked over MCP never builds it. Concurrent
// first requests build it once; a build that fails answers each request
// with that failure.
type mcpDoor struct {
	a      *App
	once   sync.Once
	served http.Handler
	err    error
	builds atomic.Int32 // how many times the SDK server was built: 0 or 1
}

func newMCPDoor(a *App) *mcpDoor { return &mcpDoor{a: a} }

func (d *mcpDoor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a := d.a
	id := a.mcpIdentity(w, r)
	if id == nil {
		return
	}
	d.once.Do(func() { d.served, d.err = buildMCPServer(a); d.builds.Add(1) })
	if d.err != nil {
		writeErr(w, 500, "the MCP server could not be built: %v", d.err)
		return
	}
	ctx := context.WithValue(r.Context(), mcpRequestKey{}, &mcpRequest{id: id, baseURL: a.mcpBaseURL(r)})
	d.served.ServeHTTP(w, r.WithContext(ctx))
}

// buildMCPServer registers the catalog's tools with the SDK and wraps its
// streamable HTTP transport in the bearer check. The SDK panics on a tool
// it cannot register; that is returned as the build's error.
func buildMCPServer(a *App) (_ http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	server := mcp.NewServer(&mcp.Implementation{Name: "apptown", Version: mcpServerVersion}, &mcp.ServerOptions{
		Instructions: mcpapi.Instructions(),
		// Tools only, and the list never changes while the process runs.
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	server.AddReceivingMiddleware(auditMCPSessions(a))
	for _, t := range mcpToolsFor(a) {
		server.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, mcpToolHandler(a, t.Name))
	}
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse:        true,
		MaxRequestBodyBytes: mcpMaxBody,
		SessionTimeout:      mcpSessionIdle,
		// The SDK refuses a loopback listener reached under another host name,
		// a guard against DNS rebinding onto an unauthenticated local server.
		// apptownd is often exactly that behind its reverse proxy, and this door
		// admits only a bearer token, never an ambient browser credential.
		DisableLocalhostProtection: true,
	})
	// The SDK binds each session to the user who opened it and refuses a
	// request from anyone else; it learns the user from this token info.
	return auth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		req, _ := ctx.Value(mcpRequestKey{}).(*mcpRequest)
		if req == nil {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: req.id.User, Extra: map[string]any{"request": req}}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(transport), nil
}

// serveMCP answers /mcp/user through the App's door.
func (a *App) serveMCP(w http.ResponseWriter, r *http.Request) { a.mcpDoor.ServeHTTP(w, r) }

// mcpIdentity resolves who is calling. Only an OAuth token or an agent
// token opens this door (design/subsystems/mcp.md §3 "Personal tokens", "Open mode"): no session cookie (a
// browser's ambient credential must not be reusable by a local agent), no
// console or run token (instruments work on one project through their own
// port), and no open-mode X-AppTown-Actor header — open mode and attributable
// remote access are incompatible, so an open-mode forge's MCP surface is
// token-only too. A bad or missing token is a 401 with the realm named so a
// client knows which credential to ask for.
func (a *App) mcpIdentity(w http.ResponseWriter, r *http.Request) *authIdentity {
	refusal := "sign in through your coding agent (it will open a browser), or use an agent token: Authorization: Bearer forge_…"
	if hasBearer(r) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// An OAuth access token audience-bound to this door (oauth.go) is the
		// designed path; an agent token is the bootstrap/compatibility one.
		if id := a.resolveOAuthToken(r.Context(), tok, identity.AudienceUser); id != nil {
			return id
		}
		if id := a.resolveBearer(r); id != nil {
			// A request here is an agent's. A cli token acts with its
			// person's full authority wherever it is presented, so it is
			// refused here: an agent holding one could take that authority
			// to /api/ and approve or land.
			if id.Purpose == access.TokenAgent {
				return id
			}
			refusal = "this is a command-line token, which acts as you; sign in through your coding agent, or make an agent token: apptown token create <name> --purpose agent"
		}
	}
	// The resource_metadata pointer is how an MCP client discovers the
	// authorization server and starts the browser flow (RFC 9728).
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="forge-mcp", resource_metadata="%s/.well-known/oauth-protected-resource%s"`,
		a.mcpBaseURL(r), mcpPath))
	writeErr(w, 401, "%s", refusal)
	return nil
}

// mcpBaseURL is the absolute origin webUrl links and the OAuth discovery
// documents are built on: AppTown's browser origin, so an agent is sent
// to the same address a person signs in at (httpapi.BrowserSession.Origin).
func (a *App) mcpBaseURL(r *http.Request) string { return a.browserOrigin(r) }

// mcpRequestOf recovers the door's record of the HTTP request a call came in on.
func mcpRequestOf(extra *mcp.RequestExtra) *mcpRequest {
	if extra == nil || extra.TokenInfo == nil {
		return nil
	}
	req, _ := extra.TokenInfo.Extra["request"].(*mcpRequest)
	return req
}

// auditMCPSessions records one audit row per session, naming the client —
// access is recorded; reads are not spammed (design/subsystems/mcp.md §10).
func auditMCPSessions(a *App) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			in, ok := req.(*mcp.ServerRequest[*mcp.InitializeParams])
			if method != "initialize" || err != nil || !ok {
				return res, err
			}
			call := mcpRequestOf(req.GetExtra())
			if call == nil {
				return res, err
			}
			client, version, proto := "unknown client", "", ""
			if in.Params != nil {
				proto = in.Params.ProtocolVersion
				if in.Params.ClientInfo != nil {
					version = in.Params.ClientInfo.Version
					if name := strings.TrimSpace(in.Params.ClientInfo.Name); name != "" {
						client = name
					}
				}
			}
			a.audit(ctx, call.id.User, ids.Nil, "mcp.session", client, map[string]any{
				"client": client, "version": version, "protocolVersion": proto, "user": call.id.User})
			return res, err
		}
	}
}

// mcpToolHandler runs a catalog tool. Two kinds of failure are kept apart on
// purpose (MCP tools/call): malformed arguments are protocol errors (JSON-RPC
// invalid params), while "no such project" is a result the model should read
// and recover from (isError). The tool is looked up per call so it binds to the
// App's current owners.
func mcpToolHandler(a *App, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		call := mcpRequestOf(req.Extra)
		if call == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the request carried no verified caller"}
		}
		tool := mcpToolsFor(a).ByName(name)
		if tool == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
		}
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("%s: arguments must be a JSON object", name)}
			}
		}
		if args == nil {
			args = map[string]any{}
		}
		if msg := tool.CheckArgs(args); msg != "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("%s: %s", name, msg)}
		}
		actor := mcpActor(ctx, a, call.id, req.Session)
		result, err := tool.Run(&mcpapi.Call{Context: callerMCPContext(ctx, call.id, actor), BaseURL: call.baseURL}, args)
		if err != nil {
			// A refusal, or any failure its owner classified (not found,
			// forbidden, conflict, ...), is something the model can read and
			// act on. Only an unclassified failure is the protocol's problem.
			var refusal mcpapi.Refusal
			switch {
			case errors.As(err, &refusal):
				return mcpRefusal(string(refusal)), nil
			case fault.KindOf(err) != fault.Internal:
				return mcpRefusal(fault.Message(err)), nil
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: fmt.Sprintf("%s failed: %v", name, err)}
		}
		pretty, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: fmt.Sprintf("%s produced an unencodable result: %v", name, err)}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(pretty)}}, StructuredContent: result}, nil
	}
}

func mcpRefusal(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// mcpActor names who acted and through which client, for receipts.
func mcpActor(ctx context.Context, a *App, id *authIdentity, session *mcp.ServerSession) string {
	client := "mcp"
	if session != nil {
		if p := session.InitializeParams(); p != nil && p.ClientInfo != nil && strings.TrimSpace(p.ClientInfo.Name) != "" {
			client = strings.TrimSpace(p.ClientInfo.Name)
		}
	}
	if client == "mcp" && !id.DelegationID.IsZero() {
		// Without a session (or before initialize) the delegation the person
		// consented to still names the client.
		if d := a.delegationByID(ctx, id.DelegationID); d != nil && d.ClientName != "" {
			client = d.ClientName
		}
	}
	return id.User + " via mcp/" + client
}
