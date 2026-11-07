package identity

import (
	"context"
	"errors"
	"testing"
)

func TestReviewPasswordCreationIsNotLastSeen(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(map[bool]string{false: "new account", true: "claimed name"}[claim], func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := New(testDB(t), Config{})
			if err := s.SeedAdministrator(ctx, "boss"); err != nil {
				t.Fatal(err)
			}
			if claim {
				if _, err := s.EnsurePerson(ctx, "newcomer"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SaveUser(ctx, AccountChange{Username: "newcomer", Password: "test-password-long"}, admittedPrincipal(t, s, "boss")); err != nil {
				t.Fatal(err)
			}
			users, err := s.Users(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range users {
				if u.Username == "newcomer" {
					if u.Acted {
						t.Fatal("fixture has activity")
					}
					if u.LastSeen.Valid {
						t.Fatalf("never-signed-in password account has last seen %v", u.LastSeen)
					}
					if !u.HasPassword {
						t.Fatal("password account lost its credential")
					}
					if err := s.RemoveName(ctx, "newcomer", admittedPrincipal(t, s, "boss")); !errors.Is(err, errRealAccount) {
						t.Fatalf("password must make the account permanent: %v", err)
					}
					return
				}
			}
			t.Fatal("missing newcomer")
		})
	}
}
