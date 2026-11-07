package identity

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAccountListingRetainsThePersonsActivityTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := New(testDB(t), Config{Now: func() time.Time { return now }})
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "lee", Password: "activity-test-password"}, admittedPrincipal(t, s, "boss")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "lee", Password: "activity-test-password"}); err != nil {
		t.Fatal(err)
	}
	activity := now
	now = now.Add(24 * time.Hour)
	// These are the administrator's acts, not activity by Lee.
	if err := s.SaveGroup(ctx, GroupChange{Name: "reviewers", Members: []string{"lee"}}, admittedPrincipal(t, s, "boss")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectMember(ctx, idstest.N(42), "lee", access.RoleProjectViewer, "boss"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		users, err := s.Users(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, u := range users {
			if u.Username == "lee" {
				found = true
				if !u.LastSeen.Valid || !u.LastSeen.Time.Equal(activity) {
					t.Fatalf("read renewed last seen: %v, want %v", u.LastSeen, activity)
				}
			}
		}
		if !found {
			t.Fatal("missing account")
		}
	}
}
