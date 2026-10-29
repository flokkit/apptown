package identity

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
)

// Fixed default-cost hash for admission-password; password hashing is not the
// behavior under test, so fixtures do not repeatedly generate it.
const admissionPasswordHash = "$2a$10$O0c4GKXCPhJTMCIS9pfUsuo3GQe2Sw67gzS/WwszVdACo6D7PkzQ6"

func admissionService(t *testing.T) *Service {
	t.Helper()
	s := New(testDB(t), Config{})
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member", PasswordHash: admissionPasswordHash}, "setup"); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPasswordAdmissionAndSessionFailure(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	for _, in := range []PasswordLogin{{Username: "unknown", Password: "admission-password"}, {Username: "alice", Password: "wrong"}} {
		result, err := s.SignIn(ctx, in)
		if !errors.Is(err, ErrCredentials) || result.Token != "" {
			t.Fatalf("bad credentials: %v", err)
		}
	}
	restore := s.db.FailNextStatement("INSERT INTO sessions")
	result, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password"})
	restore()
	var failed *SessionIssueError
	if !errors.As(err, &failed) || result.Token != "" {
		t.Fatalf("session failure: %v", err)
	}
	var n int
	// sql-guarantee: The failed sign-in handed back no token, so no owner read can name a phantom session; only the table total can prove none was stored.
	if err := s.db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("phantom session: %d %v", n, err)
	}
	result, err = s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password"})
	if err != nil || result.Token == "" || result.Principal.Name != "alice" || result.Principal.Role != "member" {
		t.Fatalf("sign in: %v", err)
	}
	pr, err := s.SessionIdentity(ctx, result.Token)
	if err != nil || pr.Name != "alice" || pr.Via != "session" {
		t.Fatalf("session identity: %+v %v", pr, err)
	}
}
func TestCredentialAdmissionReadsCurrentRole(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	session, err := issueSessionIn(context.Background(), s.db, "alice", s.sessions.ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	member, err := testIssueToken(s, "member", "alice", "member", "setup")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := testIssueToken(s, "admin", "alice", "admin", "setup")
	if err != nil {
		t.Fatal(err)
	}
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "admin"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, member, nil); err != nil || pr.Role != "member" {
		t.Fatalf("member bound: %+v %v", pr, err)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, admin, nil); err != nil || pr.Role != "admin" {
		t.Fatalf("admin bound: %+v %v", pr, err)
	}
	if pr, err := s.SessionIdentity(ctx, session); err != nil || pr.Role != "admin" {
		t.Fatalf("session role: %+v %v", pr, err)
	}
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, admin, nil); err != nil || pr.Role != "member" {
		t.Fatalf("demotion: %+v %v", pr, err)
	}
}
func TestAdministratorAdmissionPolicy(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "admin"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdministratorNetworks(ctx, access.Principal{Kind: "user", Name: "alice", Via: "session"}, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8"}, net.ParseIP("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password", From: net.ParseIP("192.0.2.1")})
	var denied *AdministratorDenied
	if !errors.As(err, &denied) {
		t.Fatalf("password network: %v", err)
	}
	token, err := testIssueToken(s, "admin", "alice", "admin", "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PersonalTokenIdentity(ctx, token, net.ParseIP("192.0.2.1")); !errors.As(err, &denied) {
		t.Fatalf("token network: %v", err)
	}
	if _, err := s.PersonalTokenIdentity(ctx, token, net.ParseIP("10.1.2.3")); err != nil {
		t.Fatalf("allowed network: %v", err)
	}
	restore := s.db.FailNextStatement("-- name: InstanceSetting")
	_, err = s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password", From: net.ParseIP("10.1.2.3")})
	restore()
	if !errors.As(err, &denied) {
		t.Fatalf("policy read: %v", err)
	}
}
func TestAdministratorNetworkValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		values  AdministratorNetworks
		ip      string
		allowed bool
	}{
		{nil, "192.0.2.1", true}, {AdministratorNetworks{"10.0.0.0/8"}, "192.0.2.1", false}, {AdministratorNetworks{"10.0.0.0/8"}, "10.1.2.3", true},
		{AdministratorNetworks{"10.0.0.0/8"}, "127.0.0.1", true}, {AdministratorNetworks{"2001:db8::1"}, "2001:db8::1", true}, {AdministratorNetworks{"bad"}, "192.0.2.1", true},
	} {
		if got := tc.values.Allows(net.ParseIP(tc.ip)); got != tc.allowed {
			t.Fatalf("networks %v from %s: %v", tc.values, tc.ip, got)
		}
	}
}
