package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/wow"
)

// The instance's pack switch keeps off packs in the store and refuses to
// turn off the last offered pack.
func TestPackEnabledSwitch(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ways := wow.New(db, wow.Dependencies{})
	authority := identity.New(db, identity.Config{})
	if err := authority.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alpha", "beta"} {
		if version, err := ways.PublishPack(t.Context(), n, n+" pack", `{}`, "stock"); err != nil || version != 1 {
			t.Fatalf("publish %s: version=%d err=%v", n, version, err)
		}
	}
	if packs, err := ways.OfferedPacks(t.Context()); err != nil || len(packs) != 2 {
		t.Fatalf("both packs should start on: %+v %v", packs, err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"}, Receipt: "bls", Verified: true})
	handler := NewPackLibrary(ways, nil, authority, db, nil)
	flip := func(name string, on bool) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"enabled":false}`
		if on {
			body = `{"enabled":true}`
		}
		req := httptest.NewRequest("POST", "/api/v1/packs/"+name+"/enabled", strings.NewReader(body)).WithContext(ctx)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handler.SetPackEnabled(rec, req)
		return rec
	}
	if rec := flip("alpha", false); rec.Code != 200 {
		t.Fatalf("turning alpha off: %d %s", rec.Code, rec.Body.String())
	}
	on, err := ways.OfferedPacks(ctx)
	if err != nil || len(on) != 1 || on[0].Name != "beta" {
		t.Fatalf("only beta should be on, got %+v %v", on, err)
	}
	pk, err := ways.Pack(ctx, "alpha")
	if err != nil || pk == nil || pk.Enabled {
		t.Fatalf("alpha should still exist, off: %+v %v", pk, err)
	}
	if rec := flip("beta", false); rec.Code != 409 {
		t.Fatalf("the last pack that is on must not turn off: %d %s", rec.Code, rec.Body.String())
	}
	if rec := flip("alpha", true); rec.Code != 200 {
		t.Fatalf("turning alpha back on: %d", rec.Code)
	}
	if on, err := ways.OfferedPacks(ctx); err != nil || len(on) != 2 {
		t.Fatalf("both packs restored: %+v %v", on, err)
	}
	if rec := flip("nope", true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown pack: %d", rec.Code)
	}
}
