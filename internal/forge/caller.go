package forge

import (
	"context"
	"net/http"

	"flokkit.io/forge/internal/access"
)

// callerContext is the HTTP boundary. A supplied name without transport
// provenance never becomes an authenticated internal command caller.
func callerContext(r *http.Request) context.Context {
	return access.WithCaller(r.Context(), access.Caller{Principal: principalOf(identityFrom(r), actor(r)), Verified: identityFrom(r) != nil, Receipt: actor(r)})
}
func verifiedContext(ctx context.Context, pr access.Principal) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: pr, Verified: true, Receipt: pr.Name})
}
func callerMCPContext(ctx context.Context, id *authIdentity, receipt string) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: principalOf(id, ""), Verified: id != nil, Receipt: receipt})
}
