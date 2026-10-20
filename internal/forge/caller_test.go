package forge

import (
	"context"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/access"
)

func TestTransportCallerSeparatesProvenanceAndReceipt(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-AppTown-Actor", "root")
	c, ok := access.CallerFrom(callerContext(r))
	if !ok || c.Verified || c.Receipt != "root" {
		t.Fatalf("header became authority: %+v", c)
	}
	r = testRequestAs(r, "person")
	c, _ = access.CallerFrom(callerContext(r))
	if !c.Verified || c.Principal.Name != "person" || c.Receipt != "person" {
		t.Fatalf("transport provenance: %+v", c)
	}
	c, _ = access.CallerFrom(callerMCPContext(context.Background(), nil, "person via client"))
	if c.Verified {
		t.Fatal("MCP receipt authenticated caller")
	}
	c, _ = access.CallerFrom(callerMCPContext(context.Background(), &authIdentity{User: "person", Via: "oauth", Scopes: []string{"apptown:user:read"}}, "person via client"))
	if !c.Verified || c.Principal.Name != "person" || c.Receipt != "person via client" {
		t.Fatalf("MCP attribution: %+v", c)
	}
}
