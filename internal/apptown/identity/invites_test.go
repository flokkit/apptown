package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func testInviteService(t *testing.T) *Service {
	t.Helper()
	s := New(testDB(t), Config{SessionTTL: time.Hour})
	testGrantRoot(t, s, "ops")
	return s
}

func receipts(t *testing.T, s *Service, kind string) []audit.Entry {
	t.Helper()
	got, err := audit.Recent(context.Background(), s.db, audit.Filter{KindPrefix: kind})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An invite lets its holder set the invited account's password once and be
// signed in; the record keeps who invited whom with what role, and the link
// is stored only as a hash.
func TestInviteSetsThePasswordOnceAndSignsIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	issued, err := s.Invite(ctx, NewInvite{Username: "kim", Label: "Kim Lee"}, admittedPrincipal(t, s, "ops"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Token, InvitePrefix) || issued.Invite.Role != "member" || issued.Invite.State != "pending" {
		t.Fatalf("issued invite: %+v", issued.Invite)
	}
	if d := time.Until(issued.Invite.ExpiresAt); d < InviteTTL-time.Minute || d > InviteTTL {
		t.Fatalf("default lifetime is seven days: %v", d)
	}
	pending, err := s.Invites(ctx, admittedPrincipal(t, s, "ops"))
	if err != nil || len(pending) != 1 || pending[0].Username != "kim" || pending[0].InvitedBy != "ops" || pending[0].Label != "Kim Lee" {
		t.Fatalf("pending invites: %+v %v", pending, err)
	}
	if b, _ := json.Marshal(pending); strings.Contains(string(b), issued.Token) || strings.Contains(string(b), HashToken(issued.Token)) {
		t.Fatal("a listed invite carries its token")
	}
	shown, err := s.InviteFor(ctx, issued.Token)
	if err != nil || shown.Username != "kim" || shown.InvitedBy != "ops" || shown.State != "pending" {
		t.Fatalf("invite link view: %+v %v", shown, err)
	}

	signed, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "kims-password"})
	if err != nil {
		t.Fatal(err)
	}
	if pr, err := s.SessionIdentity(ctx, signed.Token); err != nil || pr.Name != "kim" || pr.Role != "member" {
		t.Fatalf("accepted invite did not sign kim in: %+v %v", pr, err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "kim", Password: "kims-password"}); err != nil {
		t.Fatalf("the chosen password does not sign in: %v", err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "another-password"}); !errors.Is(err, ErrInviteUsed) {
		t.Fatalf("second use: %v", err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "kim", Password: "kims-password"}); err != nil {
		t.Fatalf("a refused second use changed the password: %v", err)
	}
	if shown, _ := s.InviteFor(ctx, issued.Token); shown.State != "accepted" {
		t.Fatalf("used invite reads as %q", shown.State)
	}
	if pending, _ := s.Invites(ctx, admittedPrincipal(t, s, "ops")); len(pending) != 0 {
		t.Fatalf("a used invite is still pending: %+v", pending)
	}

	invited := receipts(t, s, "user.invited")
	if len(invited) != 1 || invited[0].Actor != "ops" || invited[0].Subject != "kim" || !strings.Contains(string(invited[0].Payload), `"role":"member"`) {
		t.Fatalf("invite receipt: %+v", invited)
	}
	accepted := receipts(t, s, "user.invite-accepted")
	if len(accepted) != 1 || accepted[0].Actor != "kim" || !strings.Contains(string(accepted[0].Payload), `"invitedBy":"ops"`) {
		t.Fatalf("acceptance receipt: %+v", accepted)
	}
	for _, e := range receipts(t, s, "user.") {
		if strings.Contains(string(e.Payload), issued.Token) || strings.Contains(string(e.Payload), "kims-password") {
			t.Fatalf("receipt %s carries a secret", e.Kind)
		}
	}
}

// A withdrawn, replaced or expired invite says so and changes nothing.
func TestDeadInvitesAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	revoked, err := s.Invite(ctx, NewInvite{Username: "rev"}, admittedPrincipal(t, s, "ops"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, revoked.Invite.ID, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: revoked.Token, Password: "long-enough"}); !errors.Is(err, ErrInviteRevoked) {
		t.Fatalf("revoked invite: %v", err)
	}
	if got := receipts(t, s, "user.invite-revoked"); len(got) != 1 || got[0].Actor != "ops" || got[0].Subject != "rev" {
		t.Fatalf("revocation receipt: %+v", got)
	}

	lost, err := s.Invite(ctx, NewInvite{Username: "lost"}, admittedPrincipal(t, s, "ops"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invite(ctx, NewInvite{Username: "lost"}, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: lost.Token, Password: "long-enough"}); !errors.Is(err, ErrInviteRevoked) {
		t.Fatalf("a replaced invite still works: %v", err)
	}

	expired, err := s.Invite(ctx, NewInvite{Username: "late", Days: 1}, admittedPrincipal(t, s, "ops"))
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: No operation records an invite whose expiry has passed; move this one's expiry into the past to test refusal at acceptance.
	if _, err := s.db.Exec(`UPDATE invites SET expires_at = now() - interval '1 minute' WHERE id = ?`, expired.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: expired.Token, Password: "long-enough"}); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expired invite: %v", err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: InvitePrefix + "made-up", Password: "long-enough"}); !errors.Is(err, ErrInviteUnknown) {
		t.Fatalf("unknown token: %v", err)
	}
	// The invited people's accounts were recorded with their invites; a
	// refused invite gives none of them a password.
	for _, name := range []string{"rev", "lost", "late"} {
		if a, err := s.directory.login(ctx, name); err != nil || a == nil || a.HasPassword {
			t.Fatalf("a refused invite let %s sign in: %+v %v", name, a, err)
		}
	}
	if got := receipts(t, s, "user.invite-accepted"); len(got) != 0 {
		t.Fatalf("refused invites left acceptance receipts: %+v", got)
	}
}

// Only an administrator invites, and an invite works only while its inviter
// still could: a demoted inviter's invite is refused.
func TestInviteNeedsCurrentAdministrator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	if err := s.SaveUser(ctx, AccountChange{Username: "mem", Password: "member-password"}, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	member := admittedPrincipal(t, s, "mem")
	member.Role = "member"
	var denied *access.Denied
	if _, err := s.Invite(ctx, NewInvite{Username: "friend"}, member); !errors.As(err, &denied) {
		t.Fatalf("a member invited someone: %v", err)
	}
	if _, err := s.Invites(ctx, member); !errors.As(err, &denied) {
		t.Fatalf("a member listed invites: %v", err)
	}
	if got := receipts(t, s, "user.invited"); len(got) != 0 {
		t.Fatalf("a refused invite left a receipt: %+v", got)
	}
	if _, err := s.Invite(ctx, NewInvite{Username: "mem"}, admittedPrincipal(t, s, "ops")); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("invited an account that can already sign in: %v", err)
	}

	// An administrator invite grants the role in the inviter's name.
	if err := s.SaveUser(ctx, AccountChange{Username: "boss", Password: "boss-password", Role: "admin"}, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	adminInvite, err := s.Invite(ctx, NewInvite{Username: "deputy", Role: "admin"}, admittedPrincipal(t, s, "boss"))
	if err != nil {
		t.Fatal(err)
	}
	later, err := s.Invite(ctx, NewInvite{Username: "intern"}, admittedPrincipal(t, s, "boss"))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := s.AcceptInvite(ctx, InviteAcceptance{Token: adminInvite.Token, Password: "deputy-password"})
	if err != nil || signed.Principal.Role != "admin" {
		t.Fatalf("administrator invite: %+v %v", signed.Principal, err)
	}
	grants, err := ActiveGrantsIn(ctx, s.db, access.Principal{Kind: "user", Name: "deputy"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if g, root := access.RootGrant(grants); !root || g.GrantedBy != "boss" {
		t.Fatalf("deputy's administrator grant: %+v %v", g, root)
	}

	if err := s.SaveUser(ctx, AccountChange{Username: "boss", Role: "member"}, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: later.Token, Password: "intern-password"}); !errors.Is(err, ErrInviterGone) {
		t.Fatalf("a demoted inviter's invite still works: %v", err)
	}
	if shown, _ := s.InviteFor(ctx, later.Token); shown.State != "pending" {
		t.Fatalf("a refused acceptance consumed the invite: %q", shown.State)
	}
}

// The administrator networks bind an invite whenever accepting it would sign
// in an administrator: from an admin invite, or from authority the account
// gained after a member invite was made, directly or through a group. A
// refusal leaves the invite usable from an allowed network.
func TestInviteRespectsEffectiveAdministratorNetworks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	if _, err := s.SetAdministratorNetworks(ctx, ops, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8"}, net.ParseIP("10.1.2.3")); err != nil {
		t.Fatal(err)
	}
	outside, inside := net.ParseIP("203.0.113.20"), net.ParseIP("10.4.5.6")
	accept := func(token, who string) (SignedIn, error) {
		return s.AcceptInvite(ctx, InviteAcceptance{Token: token, Password: who + "-password", From: outside})
	}
	refused := func(name, token string, err error, signed SignedIn) {
		t.Helper()
		var denied *AdministratorDenied
		if !errors.As(err, &denied) || signed.Token != "" {
			t.Fatalf("%s accepted outside the administrator networks: role=%q err=%v", name, signed.Principal.Role, err)
		}
		if shown, _ := s.InviteFor(ctx, token); shown.State != "pending" {
			t.Fatalf("%s: a refused acceptance consumed the invite: %q", name, shown.State)
		}
		if a, _ := s.directory.login(ctx, name); a != nil && a.HasPassword {
			t.Fatalf("%s: a refused acceptance set the password", name)
		}
	}

	// An admin invite.
	boss, err := s.Invite(ctx, NewInvite{Username: "boss", Role: "admin"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := accept(boss.Token, "boss")
	refused("boss", boss.Token, err, signed)

	// A member invite whose account became an administrator directly.
	kim, err := s.Invite(ctx, NewInvite{Username: "kim"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Role: "admin"}, ops); err != nil {
		t.Fatal(err)
	}
	signed, err = accept(kim.Token, "kim")
	refused("kim", kim.Token, err, signed)

	// A member invite whose account became an administrator through a group.
	lee, err := s.Invite(ctx, NewInvite{Username: "lee"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "leads", Members: []string{"lee"}}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGrant(ctx, Spec{SubjectType: "group", Subject: "leads", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "ops"}); err != nil {
		t.Fatal(err)
	}
	signed, err = accept(lee.Token, "lee")
	refused("lee", lee.Token, err, signed)

	// From inside the networks the same invite still works.
	signed, err = s.AcceptInvite(ctx, InviteAcceptance{Token: lee.Token, Password: "lee-password", From: inside})
	if err != nil || signed.Principal.Role != "admin" {
		t.Fatalf("inside acceptance: %+v %v", signed.Principal, err)
	}
}

// A member invite is refused for a name that already holds administrator
// authority, directly or through a group, so an invite's role is what its
// holder gets.
func TestMemberInviteRefusedForAnAdministrator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Role: "admin"}, ops); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "leads", Members: []string{"lee"}}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGrant(ctx, Spec{SubjectType: "group", Subject: "leads", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "ops"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kim", "lee"} {
		if _, err := s.Invite(ctx, NewInvite{Username: name}, ops); !errors.Is(err, ErrAlreadyAdmin) {
			t.Fatalf("member invite for administrator %s: %v", name, err)
		}
		if _, err := s.Invite(ctx, NewInvite{Username: name, Role: "admin"}, ops); err != nil {
			t.Fatalf("admin invite for administrator %s: %v", name, err)
		}
	}
	if got := receipts(t, s, "user.invited"); len(got) != 2 {
		t.Fatalf("refused member invites left receipts: %d", len(got))
	}
}

// A password reset link replaces an existing account's password: until it
// is used the old password and sessions keep working; once used, the old
// password and every session the account had stop working, its holder is
// signed in with the new one, and the account's authority is exactly what
// it was — a reset grants nothing and takes nothing away. The receipts say
// who made the link and that it was used, and never carry the token.
func TestPasswordLinkReplacesThePasswordAndEndsEverySession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	// One administrator and one member with a group's project role, so a
	// reset that granted or revoked anything would show in either.
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Password: "kims-old-password", Role: "admin"}, ops); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "lee", Password: "lees-old-password"}, ops); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "shop-team", Members: []string{"lee"}}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGrant(ctx, Spec{SubjectType: "group", Subject: "shop-team", Role: access.RoleProjectContrib, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "ops"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, role string }{{"kim", "admin"}, {"lee", "member"}} {
		old := c.name + "s-old-password"
		before, err := ActiveGrantsIn(ctx, s.db, access.Principal{Kind: "user", Name: c.name}, time.Now())
		if err != nil || len(before) == 0 {
			t.Fatalf("%s grants before: %+v %v", c.name, before, err)
		}
		var sessions []string
		for range 2 {
			signed, err := s.SignIn(ctx, PasswordLogin{Username: c.name, Password: old})
			if err != nil {
				t.Fatal(err)
			}
			sessions = append(sessions, signed.Token)
		}

		issued, err := s.PasswordLink(ctx, NewPasswordLink{Username: c.name}, ops)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(issued.Token, InvitePrefix) || issued.Invite.Purpose != PurposePassword || issued.Invite.Role != "" || issued.Invite.State != "pending" {
			t.Fatalf("issued link: %+v", issued.Invite)
		}
		if d := time.Until(issued.Invite.ExpiresAt); d < PasswordLinkTTL-time.Minute || d > PasswordLinkTTL {
			t.Fatalf("default lifetime is three days: %v", d)
		}
		// Making the link changes nothing until it is used.
		if pr, err := s.SessionIdentity(ctx, sessions[0]); err != nil || pr.Name != c.name {
			t.Fatalf("making a link ended %s's session: %+v %v", c.name, pr, err)
		}
		if _, err := s.SignIn(ctx, PasswordLogin{Username: c.name, Password: old}); err != nil {
			t.Fatalf("making a link stopped %s's password: %v", c.name, err)
		}
		if shown, err := s.InviteFor(ctx, issued.Token); err != nil || shown.Purpose != PurposePassword || shown.InvitedBy != "ops" {
			t.Fatalf("link view: %+v %v", shown, err)
		}

		signed, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: c.name + "s-new-password"})
		if err != nil {
			t.Fatal(err)
		}
		if pr, err := s.SessionIdentity(ctx, signed.Token); err != nil || pr.Name != c.name || pr.Role != c.role {
			t.Fatalf("the reset did not sign %s in as %s: %+v %v", c.name, c.role, pr, err)
		}
		for _, tok := range sessions {
			if pr, err := s.SessionIdentity(ctx, tok); err == nil && pr.Name != "" {
				t.Fatalf("an old session of %s still works after the reset", c.name)
			}
		}
		if _, err := s.SignIn(ctx, PasswordLogin{Username: c.name, Password: old}); err == nil {
			t.Fatalf("%s's old password still works", c.name)
		}
		if _, err := s.SignIn(ctx, PasswordLogin{Username: c.name, Password: c.name + "s-new-password"}); err != nil {
			t.Fatalf("%s's new password does not sign in: %v", c.name, err)
		}
		after, err := ActiveGrantsIn(ctx, s.db, access.Principal{Kind: "user", Name: c.name}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if b, a := grantFacts(before), grantFacts(after); b != a {
			t.Fatalf("the reset changed %s's grants:\nbefore %s\nafter  %s", c.name, b, a)
		}
		if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "third-password"}); !errors.Is(err, ErrInviteUsed) {
			t.Fatalf("second use: %v", err)
		}

		made := receipts(t, s, "user.password-link")
		var issuedBy, used bool
		for _, e := range made {
			switch {
			case e.Kind == "user.password-link" && e.Actor == "ops" && e.Subject == c.name:
				issuedBy = true
			case e.Kind == "user.password-link-used" && e.Actor == c.name && strings.Contains(string(e.Payload), `"issuedBy":"ops"`):
				used = true
			}
		}
		if !issuedBy || !used {
			t.Fatalf("%s's receipts: %+v", c.name, made)
		}
		for _, e := range receipts(t, s, "") {
			if p := string(e.Payload); strings.Contains(p, issued.Token) || strings.Contains(p, HashToken(issued.Token)) || strings.Contains(p, "new-password") {
				t.Fatalf("receipt %s carries a secret", e.Kind)
			}
		}
	}
}

// grantFacts is a grant list as comparable text: which grant, who holds
// what, where, from whom. The id makes a revoked-and-regranted role differ.
func grantFacts(gs []access.Grant) string {
	var out []string
	for _, g := range gs {
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s", g.ID, g.SubjectType, g.Subject, g.RoleName, g.Capability, g.ScopeType, g.ScopeID, g.GrantedBy))
	}
	return strings.Join(out, "; ")
}

// Only an administrator, signed in as themselves, makes a password reset
// link, and only for an enabled account that has a password. A link that
// was withdrawn, replaced, has expired, or whose maker is no longer an
// administrator changes nothing; neither does one for an account disabled
// after it was made.
func TestPasswordLinkRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	for _, name := range []string{"kim", "mem", "off", "late", "lost", "rev", "boss"} {
		role := ""
		if name == "boss" {
			role = "admin"
		}
		if err := s.SaveUser(ctx, AccountChange{Username: name, Password: name + "-password", Role: role}, ops); err != nil {
			t.Fatal(err)
		}
	}
	unchanged := func(name string) {
		t.Helper()
		if _, err := s.SignIn(ctx, PasswordLogin{Username: name, Password: name + "-password"}); err != nil {
			t.Fatalf("a refused link changed %s's password: %v", name, err)
		}
	}

	var denied *access.Denied
	member := admittedPrincipal(t, s, "mem")
	member.Role = "member"
	agent := admittedPrincipal(t, s, "ops")
	agent.Via, agent.Purpose = "token", access.TokenAgent
	for _, pr := range []access.Principal{member, agent} {
		if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "kim"}, pr); !errors.As(err, &denied) {
			t.Fatalf("%s via %s made a password reset link: %v", pr.Name, pr.Via, err)
		}
	}
	if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "nobody"}, ops); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("a link for a name with no account: %v", err)
	}
	if _, err := s.EnsureUser(ctx, "seen"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "seen"}, ops); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("a link for a name without a password: %v", err)
	}
	if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "kim", Days: 31}, ops); err == nil {
		t.Fatal("a link longer than 30 days was made")
	}
	disabled := true
	if err := s.SaveUser(ctx, AccountChange{Username: "off", Disabled: &disabled}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "off"}, ops); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("a link for a disabled account: %v", err)
	}
	if got := receipts(t, s, "user.password-link"); len(got) != 0 {
		t.Fatalf("refused links left receipts: %+v", got)
	}

	// Disabled after the link was made: refused, and the link stays pending.
	kim, err := s.PasswordLink(ctx, NewPasswordLink{Username: "kim"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Disabled: &disabled}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: kim.Token, Password: "new-password"}); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("a reset of a disabled account: %v", err)
	}
	if shown, _ := s.InviteFor(ctx, kim.Token); shown.State != "pending" {
		t.Fatalf("a refused reset consumed the link: %q", shown.State)
	}

	late, err := s.PasswordLink(ctx, NewPasswordLink{Username: "late", Days: 1}, ops)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: No operation records a link whose expiry has passed; move this one's expiry into the past to test refusal at use.
	if _, err := s.db.Exec(`UPDATE invites SET expires_at = now() - interval '1 minute' WHERE id = ?`, late.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: late.Token, Password: "new-password"}); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expired link: %v", err)
	}

	lost, err := s.PasswordLink(ctx, NewPasswordLink{Username: "lost"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PasswordLink(ctx, NewPasswordLink{Username: "lost"}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: lost.Token, Password: "new-password"}); !errors.Is(err, ErrInviteRevoked) {
		t.Fatalf("a replaced link still works: %v", err)
	}

	rev, err := s.PasswordLink(ctx, NewPasswordLink{Username: "rev"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, rev.Invite.ID, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: rev.Token, Password: "new-password"}); !errors.Is(err, ErrInviteRevoked) {
		t.Fatalf("withdrawn link: %v", err)
	}

	// A link works only while its maker is still an administrator.
	demoted, err := s.PasswordLink(ctx, NewPasswordLink{Username: "mem"}, admittedPrincipal(t, s, "boss"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "boss", Role: "member"}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: demoted.Token, Password: "new-password"}); !errors.Is(err, ErrInviterGone) {
		t.Fatalf("a demoted administrator's link still works: %v", err)
	}

	for _, name := range []string{"late", "lost", "rev", "mem"} {
		unchanged(name)
	}
	if got := receipts(t, s, "user.password-link-used"); len(got) != 0 {
		t.Fatalf("refused links left use receipts: %+v", got)
	}
}

// A login that verified the old password just before a reset replaced it
// does not get a session once the reset has committed: the login is paused
// between checking the password it read and starting its session (the two
// halves of SignIn), the reset runs to completion, and the login is then
// refused and leaves no session behind.
func TestLoginRacingAPasswordResetGetsNoSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Password: "kims-old-password"}, ops); err != nil {
		t.Fatal(err)
	}
	verified, err := s.verifyLogin(ctx, PasswordLogin{Username: "kim", Password: "kims-old-password"})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.PasswordLink(ctx, NewPasswordLink{Username: "kim"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "kims-new-password"}); err != nil {
		t.Fatal(err)
	}
	// The session is issued in the same transaction as the recheck, so a
	// refusal leaves nothing behind and returns no cookie value.
	signed, err := s.passwordSession(ctx, verified)
	if !errors.Is(err, ErrCredentials) || signed.Token != "" {
		t.Fatalf("a login verified against the old password got a session after the reset: %v", err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "kim", Password: "kims-new-password"}); err != nil {
		t.Fatalf("the new password does not sign in: %v", err)
	}
}

// The other interleaving: the login rechecks while a password change is
// replacing the password but has not committed. The login waits for the
// account's row lock and is then refused, instead of reading the
// still-committed old hash and starting a session the change's deletion
// never saw.
func TestLoginDuringAnUncommittedPasswordChangeWaitsAndIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	if err := s.SaveUser(ctx, AccountChange{Username: "kim", Password: "kims-old-password"}, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	verified, err := s.verifyLogin(ctx, PasswordLogin{Username: "kim", Password: "kims-old-password"})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashPassword("kims-new-password")
	if err != nil {
		t.Fatal(err)
	}
	changed, release := make(chan struct{}), make(chan struct{})
	changeDone := make(chan error, 1)
	go func() {
		changeDone <- s.db.InTx(ctx, func(tx *database.Tx) error {
			if err := changeAccountIn(ctx, tx, accountEdit{Name: "kim", PasswordHash: hash}, "kim", time.Now()); err != nil {
				return err
			}
			close(changed)
			<-release
			return nil
		})
	}()
	<-changed
	type result struct {
		signed SignedIn
		err    error
	}
	login := make(chan result, 1)
	go func() {
		signed, err := s.passwordSession(ctx, verified)
		login <- result{signed, err}
	}()
	// The login must still be waiting on the change's lock. Without the lock
	// it would read the committed old hash and finish at once.
	select {
	case r := <-login:
		t.Fatalf("the login finished while the change was uncommitted: token=%v err=%v", r.signed.Token != "", r.err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-changeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-login:
		if !errors.Is(r.err, ErrCredentials) || r.signed.Token != "" {
			t.Fatalf("a login verified against the replaced password got a session: %v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the login never finished")
	}
}

// A link the identity cutover could not tie to its person, or to its maker,
// acts on no one: it never falls back to whoever holds the name now.
func TestALinkNamingNoPrincipalActsOnNoOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	ops := admittedPrincipal(t, s, "ops")
	for _, c := range []struct {
		column string
		want   error
	}{{"principal_id", ErrInviteUnknown}, {"invited_by_principal", ErrInviterGone}} {
		name := "kim-" + strings.ReplaceAll(c.column, "_", "-")
		if err := s.SaveUser(ctx, AccountChange{Username: name, Password: "kims-old-password"}, ops); err != nil {
			t.Fatal(err)
		}
		issued, err := s.PasswordLink(ctx, NewPasswordLink{Username: name}, ops)
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: no operation makes a link without its principals; the cutover leaves one so when it cannot place an account, which this stands for.
		if _, err := s.db.Exec(`UPDATE invites SET `+c.column+` = NULL WHERE id = ?`, issued.Invite.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "a-new-password"}); !errors.Is(err, c.want) {
			t.Fatalf("a link with no %s: %v", c.column, err)
		}
	}
}

// The administrator an admin invite makes is granted by the invite's maker
// as the invite recorded them.
func TestAnAdminInvitesGrantNamesItsMaker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	testGrantRoot(t, s, "alice")
	maker, err := queries(s.db).UserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.Invite(ctx, NewInvite{Username: "newadmin", Role: "admin"}, access.Principal{ID: maker.PrincipalID, Kind: "user", Name: "alice", Via: "session", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, InviteAcceptance{Token: issued.Token, Password: "long-enough"}); err != nil {
		t.Fatal(err)
	}
	grants, err := GrantsAtIn(ctx, s.db, access.ScopeInstance, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Subject == "newadmin" && g.RoleName == access.RoleInstanceAdmin {
			if g.GrantedByPrincipal != maker.PrincipalID {
				t.Fatalf("the grant names %s, not its maker %s", g.GrantedByPrincipal, maker.PrincipalID)
			}
			return
		}
	}
	t.Fatalf("no administrator grant for newadmin: %+v", grants)
}
