package identity

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/stamp"
)

func TestAccountTransitions(t *testing.T) {
	t.Parallel()
	a := Account{}
	if followup := a.setPassword(""); followup.EndSessions || a.HasPassword {
		t.Fatal("absent password changed account")
	}
	if followup := a.setPassword("first hash"); !followup.EndSessions || !a.HasPassword || a.passwordHash != "first hash" {
		t.Fatal("password did not request session termination")
	}
	if followup := a.setPassword(""); followup.EndSessions || a.passwordHash != "first hash" || !a.HasPassword {
		t.Fatal("empty edit erased password")
	}
	if followup := a.setPassword("second hash"); !followup.EndSessions || a.passwordHash != "second hash" {
		t.Fatal("password reset")
	}
}

func TestGrantObject(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	g := access.Grant{ID: idstest.N(12), Capability: access.CapProjectRead, ScopeType: access.ScopeProject, ScopeID: idstest.N(7).String()}
	if !g.Live(now) || !g.Applies(access.CapProjectRead, access.Resource{Type: "project", ProjectID: idstest.N(7)}) {
		t.Fatal("live matching grant did not apply")
	}
	if g.Applies(access.CapProjectRead, access.Resource{Type: "project", ProjectID: idstest.N(8)}) || g.Applies(access.CapChangeCreate, access.Resource{Type: "project", ProjectID: idstest.N(7)}) {
		t.Fatal("grant widened scope or capability")
	}
	g.ExpiresAt = stamp.Of(now)
	if g.Live(now) || !g.Live(now.Add(-time.Second)) {
		t.Fatal("expiry boundary")
	}
	event, err := g.Revoke(access.Principal{Name: "alice"}, now)
	if err != nil || event != (access.Event{Kind: "grant.revoked", Actor: "alice", Subject: idstest.N(12).String()}) || !g.RevokedAt.Time.Equal(now) || g.Live(now.Add(-time.Second)) {
		t.Fatalf("expired grant revocation: %+v %v", event, err)
	}
	if _, err := g.Revoke(access.Principal{Name: "bob"}, now.Add(time.Hour)); !errors.Is(err, access.ErrGrantRevoked) || !g.RevokedAt.Time.Equal(now) {
		t.Fatal("second revocation changed the receipt time")
	}
	role := access.Grant{RoleName: access.RoleProjectOwner, ScopeType: access.ScopeProject, ScopeID: idstest.N(7).String()}
	if !role.Applies(access.CapProjectRead, access.Resource{Type: "project", ProjectID: idstest.N(7)}) {
		t.Fatal("role capability not applied")
	}
}

func TestDelegationBounds(t *testing.T) {
	t.Parallel()
	d := Delegation{ID: idstest.N(3), User: "alice", Scopes: []string{"apptown:user:read", "apptown:change:create"}}
	pr := access.Principal{Kind: "user", Name: "alice", Role: "member", Via: "session"}
	got := d.bounds(pr, time.Now())
	if got.Name != pr.Name || got.Role != pr.Role || got.Via != "oauth" || got.DelegationID != idstest.N(3) || !reflect.DeepEqual(got.Scopes, d.Scopes) {
		t.Fatalf("bound: %+v", got)
	}
	got.Scopes[0] = "changed"
	if d.Scopes[0] != "apptown:user:read" {
		t.Fatal("scope slice shared with caller")
	}
	pr.Via, pr.DelegationID, pr.Scopes = "oauth", idstest.N(2), []string{"apptown:user:read"}
	if got := d.bounds(pr, time.Now()); !reflect.DeepEqual(got.Scopes, pr.Scopes) {
		t.Fatalf("widened prior delegation: %+v", got)
	}
	pr.Scopes = nil
	if got := d.bounds(pr, time.Now()); len(got.Scopes) != 0 {
		t.Fatal("empty prior bound widened")
	}
	for _, wrong := range []access.Principal{{Kind: "user", Name: "bob"}, {Kind: "instrument", Name: "alice"}, {}} {
		if got := d.bounds(wrong, time.Now()); got.Name != "" {
			t.Fatal("delegation changed principal")
		}
	}
	d.RevokedAt = stamp.Of(time.Now())
	if got := d.bounds(access.Principal{Kind: "user", Name: "alice"}, time.Now()); got.Name != "" {
		t.Fatal("revoked delegation applied")
	}
	d.RevokedAt, d.ExpiresAt = stamp.Time{}, stamp.Of(time.Now().Add(-time.Hour))
	if got := d.bounds(access.Principal{Kind: "user", Name: "alice"}, time.Now()); got.Name != "" {
		t.Fatal("expired delegation applied")
	}
}

func TestDelegationRotation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	d := Delegation{Scopes: []string{"apptown:user:read"}, refresh: refreshCredential{hash: HashToken("presented"), expires: now.Add(time.Hour)}}
	if _, err := d.rotate("wrong", time.Now()); !errors.Is(err, ErrUnknownRefresh) || d.refresh.revoked.Valid {
		t.Fatal("wrong credential consumed the token")
	}
	pair, err := d.rotate("presented", time.Now())
	if err != nil || !strings.HasPrefix(pair.Access, AccessPrefix) || !strings.HasPrefix(pair.Refresh, refreshPrefix) || !d.refresh.revoked.Valid {
		t.Fatalf("rotation: %+v %v", pair, err)
	}
	pair.Scopes[0] = "changed"
	if d.Scopes[0] != "apptown:user:read" {
		t.Fatal("pair shares scopes")
	}
	d.ExpiresAt = stamp.Of(now.Add(-time.Hour))
	if _, err := d.rotate("presented", time.Now()); !errors.Is(err, ErrLeaked) {
		t.Fatalf("reuse after expiry is still a leak: %v", err)
	}
	d.refresh.revoked = stamp.Time{}
	if _, err := d.rotate("presented", time.Now()); !errors.Is(err, ErrConnectionEnded) || d.refresh.revoked.Valid {
		t.Fatal("expired connection consumed token")
	}
	d.ExpiresAt, d.RevokedAt = stamp.Time{}, stamp.Of(time.Now())
	if _, err := d.rotate("presented", time.Now()); !errors.Is(err, ErrConnectionEnded) {
		t.Fatal("revoked connection rotated")
	}
	d.RevokedAt, d.refresh.expires = stamp.Time{}, now.Add(-time.Hour)
	if _, err := d.rotate("presented", time.Now()); !errors.Is(err, ErrConnectionEnded) {
		t.Fatal("expired refresh token rotated")
	}
	d.end(now)
	d.end(now.Add(time.Hour))
	if !d.RevokedAt.Time.Equal(now) {
		t.Fatal("ending twice changed the revocation time")
	}
}

func TestTicketConsumption(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	// Admission already checked expiry. A push may finish after that deadline.
	ticket := Ticket{ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)}
	if err := ticket.consume(now); err != nil || !ticket.ConsumedAt.Time.Equal(now) {
		t.Fatal("admitted push could not finish")
	}
	if err := ticket.consume(now.Add(time.Hour)); !errors.Is(err, errTicketConsumed) || !ticket.ConsumedAt.Time.Equal(now) {
		t.Fatal("ticket consumed twice")
	}
	revoked := Ticket{RevokedAt: stamp.Of(now)}
	if err := revoked.consume(now); !errors.Is(err, errTicketRevoked) || revoked.ConsumedAt.Valid {
		t.Fatal("revoked ticket consumed")
	}
}
