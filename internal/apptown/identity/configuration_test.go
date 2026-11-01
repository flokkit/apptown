package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
)

func configurationFixture(t *testing.T) (*Service, access.Principal) {
	t.Helper()
	s := New(testDB(t), Config{})
	if _, err := s.EnsureUser(context.Background(), "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "operator", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "setup"}, ""); err != nil {
		t.Fatal(err)
	}
	return s, access.Principal{Kind: "user", Name: "operator", Via: "session", Role: "member"}
}

func TestConfigurationCommandsRequireCapability(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"networks"} {
		for _, caller := range []string{"root grant", "role only", "disabled", "oauth", "instrument", "missing", "revoked"} {
			t.Run(op+"/"+caller, func(t *testing.T) {
				t.Parallel()
				s, pr := configurationFixture(t)
				switch caller {
				case "role only":
					if _, err := s.EnsureUser(context.Background(), "bare"); err != nil {
						t.Fatal(err)
					}
					pr.Name, pr.Role = "bare", "admin"
				case "disabled":
					disableStoredAccount(t, s.db, "operator")
				case "oauth":
					pr.Via, pr.Scopes = "oauth", access.ScopeNames()
				case "instrument":
					pr.Kind, pr.Instrument, pr.Person = "instrument", "console", "operator"
				case "missing":
					pr = access.Principal{}
				case "revoked":
					if err := s.SeedAdministrator(context.Background(), "other-root"); err != nil {
						t.Fatal(err)
					}
					grants, err := s.ActiveGrants(context.Background(), pr)
					if err != nil {
						t.Fatal(err)
					}
					for _, g := range grants {
						if _, err := s.Revoke(context.Background(), g.ID, "setup"); err != nil {
							t.Fatal(err)
						}
					}
				}
				_, err := s.SetAdministratorNetworks(context.Background(), pr, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8"}, net.ParseIP("10.2.3.4"))
				if caller == "root grant" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var denied *access.Denied
					if !errors.As(err, &denied) && !errors.Is(err, access.ErrPrincipalRequired) {
						t.Fatalf("expected refusal: %v", err)
					}
					if networks, err := s.AdministratorNetworks(context.Background()); err != nil || len(networks) != 0 {
						t.Fatalf("denied write: %v %v", networks, err)
					}
					var settings int
					// sql-guarantee: No owner read lists instance settings; a refused or failed change must store no row at all, and an empty stored value reads the same as none through AdministratorNetworks.
					if err := s.db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM instance_settings WHERE key<>'setup_completed'`).Scan(&settings); err != nil || settings != 0 {
						t.Fatalf("denied write: %d settings stored: %v", settings, err)
					}
				}
			})
		}
	}
}

func TestAdministratorNetworkChangeRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		in               AdministratorNetworks
		ip               string
		want             AdministratorNetworks
		invalid, lockout bool
	}{
		{name: "trim preserve order duplicates", in: AdministratorNetworks{" ", " 10.0.0.0/8 ", "10.0.0.0/8", "2001:db8::1"}, ip: "10.2.3.4", want: AdministratorNetworks{"10.0.0.0/8", "10.0.0.0/8", "2001:db8::1"}},
		{name: "single IPv4", in: AdministratorNetworks{"192.0.2.1"}, ip: "192.0.2.1", want: AdministratorNetworks{"192.0.2.1"}},
		{name: "IPv6 network", in: AdministratorNetworks{"2001:db8::/32"}, ip: "2001:db8::2", want: AdministratorNetworks{"2001:db8::/32"}},
		{name: "empty anywhere", ip: "203.0.113.1"},
		{name: "loopback", in: AdministratorNetworks{"10.0.0.0/8"}, ip: "::1", want: AdministratorNetworks{"10.0.0.0/8"}},
		{name: "unknown address", in: AdministratorNetworks{"10.0.0.0/8"}, want: AdministratorNetworks{"10.0.0.0/8"}},
		{name: "bad entry", in: AdministratorNetworks{"10.0.0.0/99"}, invalid: true},
		{name: "lockout", in: AdministratorNetworks{"10.0.0.0/8"}, ip: "203.0.113.1", lockout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.in.prepare(net.ParseIP(tc.ip))
			var invalid *networkInputError
			var lockout *networkLockoutError
			switch {
			case tc.invalid:
				if !errors.As(err, &invalid) {
					t.Fatal(err)
				}
			case tc.lockout:
				if !errors.As(err, &lockout) {
					t.Fatal(err)
				}
			default:
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("%v %v", got, err)
				}
				if len(got) > 0 {
					got[0] = "changed"
					if tc.in[0] == "changed" {
						t.Fatal("input alias")
					}
				}
			}
		})
	}
}

func TestConfigurationFailuresLeaveNoChange(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"networks"} {
		for _, failure := range []string{"facts", "lock", "write", "receipt", "skipped write", "skipped receipt", "invalid", "cancelled", "before"} {
			t.Run(op+"/"+failure, func(t *testing.T) {
				t.Parallel()
				s, pr := configurationFixture(t)
				ctx := context.Background()
				marker := map[string]string{"facts": "-- name: UserByName :one", "lock": "-- name: LockInstanceSetting", "write": "-- name: PutInstanceSetting", "receipt": "INSERT INTO audit_log", "before": "-- name: InstanceSetting :one"}[failure]
				restore := func() {}
				if marker != "" {
					restore = dbtest.NextStatementFailure(s.db, marker)
				}
				if failure == "skipped write" {
					restore = vetoGroupWrite(t, s.db, "instance_settings", "INSERT")
				}
				if failure == "skipped receipt" {
					restore = vetoGroupWrite(t, s.db, "audit_log", "INSERT")
				}
				if failure == "cancelled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				call := func(ctx context.Context, invalid bool) error {
					list := AdministratorNetworks{"10.0.0.0/8"}
					if invalid {
						list = AdministratorNetworks{"no-network"}
					}
					_, err := s.SetAdministratorNetworks(ctx, pr, &AdministratorNetworks{}, list, net.ParseIP("10.1.2.3"))
					return err
				}
				if err := call(ctx, failure == "invalid"); err == nil {
					t.Fatal("accepted failed change")
				}
				restore()
				if networks, err := s.AdministratorNetworks(context.Background()); err != nil || len(networks) != 0 {
					t.Fatalf("changed setting: %v %v", networks, err)
				}
				var settings int
				// sql-guarantee: No owner read lists instance settings; a refused or failed change must store no row at all, and an empty stored value reads the same as none through AdministratorNetworks.
				if err := s.db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM instance_settings WHERE key<>'setup_completed'`).Scan(&settings); err != nil || settings != 0 {
					t.Fatalf("changed setting: %d settings stored: %v", settings, err)
				}
				if receipts := ledger(t, s.db, "auth.admin-networks"); len(receipts) != 0 {
					t.Fatalf("false mutation receipt: %+v", receipts)
				}
				if err := call(context.Background(), false); err != nil {
					t.Fatalf("retry: %v", err)
				}
			})
		}
	}
}

func TestAdministratorNetworkReceiptsFollowConcurrentWrites(t *testing.T) {
	t.Parallel()
	s, pr := configurationFixture(t)
	const n = 8
	start := make(chan struct{})
	results := make(chan error, n)
	for i := range n {
		go func(i int) {
			<-start
			// Each writer re-reads and tries again when another saved first,
			// as a person answering the conflict would.
			for {
				current, err := s.AdministratorNetworks(context.Background())
				if err != nil {
					results <- err
					return
				}
				_, err = s.SetAdministratorNetworks(context.Background(), pr, &current, AdministratorNetworks{fmt.Sprintf("10.%d.0.0/16", i)}, net.ParseIP("127.0.0.1"))
				if !errors.Is(err, errAdministratorNetworksMoved) {
					results <- err
					return
				}
			}
		}(i)
	}
	close(start)
	for range n {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	receipts := ledger(t, s.db, "auth.admin-networks")
	before := ""
	count := 0
	for i := len(receipts) - 1; i >= 0; i-- {
		subject, actor := receipts[i].Subject, receipts[i].Actor
		var payload struct {
			Before []string
			From   string
		}
		if err := json.Unmarshal(receipts[i].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if strings.Join(payload.Before, ",") != before || payload.From != "127.0.0.1" || actor != pr.Name {
			t.Fatalf("incoherent receipt: %+v, previous %s, actor %s", payload, before, actor)
		}
		before = subject
		count++
	}
	if count != n {
		t.Fatalf("receipts: %d", count)
	}
	policy, err := s.AdministratorNetworks(context.Background())
	if err != nil || strings.Join(policy, ",") != before {
		t.Fatalf("final policy: %v %v", policy, err)
	}
}

// An edit of the administrator networks names the list it started from; a
// list that changed since is refused, and so is an edit that names none.
func TestAdministratorNetworksEditNeedsTheListItStartedFrom(t *testing.T) {
	t.Parallel()
	s, pr := configurationFixture(t)
	ctx := context.Background()
	from := net.ParseIP("10.1.2.3")
	if _, err := s.SetAdministratorNetworks(ctx, pr, nil, AdministratorNetworks{"10.0.0.0/8"}, from); !errors.Is(err, errAdministratorNetworksExpected) {
		t.Fatalf("edit without a starting list: %v", err)
	}
	if _, err := s.SetAdministratorNetworks(ctx, pr, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8"}, from); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdministratorNetworks(ctx, pr, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8", "192.0.2.0/24"}, from); !errors.Is(err, errAdministratorNetworksMoved) {
		t.Fatalf("edit from an older list: %v", err)
	}
	if got, err := s.AdministratorNetworks(ctx); err != nil || len(got) != 1 {
		t.Fatalf("a refused edit wrote: %v %v", got, err)
	}
}
