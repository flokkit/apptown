package identity

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A person stopped at an approval sees whom to ask: a role resolves to the
// people and groups that hold it on the project, a group to its current
// members (a removed member is gone), and the built-in group to everyone.
func TestApproversResolveRolesAndGroupsToCurrentPeople(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	s := New(testDB(t), Config{Now: func() time.Time { return now }})
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	boss := admittedPrincipal(t, s, "boss")
	if err := s.SaveGroup(ctx, GroupChange{Name: "reviewers", Members: []string{"lee", "mei", "ana"}}, boss); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveGroupMember(ctx, "reviewers", "ana", boss); err != nil {
		t.Fatal(err)
	}
	project, other := idstest.N(7), idstest.N(8)
	for _, grantee := range []string{"priya", "group:reviewers"} {
		if err := s.AddProjectMember(ctx, project, grantee, access.RoleProjectApprover, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	// Holding the role on another project does not make anyone an approver here.
	if err := s.AddProjectMember(ctx, other, "tom", access.RoleProjectApprover, "boss"); err != nil {
		t.Fatal(err)
	}
	got, err := ApproversIn(ctx, s.db, project, []string{"role:" + access.RoleProjectApprover, "group:reviewers", "sam", "group:" + GroupAllUsers}, now)
	if err != nil {
		t.Fatal(err)
	}
	reviewers := Approver{Kind: "group", Name: "reviewers", Label: "reviewers", People: []string{"lee", "mei"}}
	want := []Approver{
		{Kind: "role", Name: access.RoleProjectApprover, Label: access.RoleByName(access.RoleProjectApprover).Title, People: []string{"priya"}, Groups: []Approver{reviewers}},
		reviewers,
		{Kind: "person", Name: "sam", Label: "sam", People: []string{"sam"}},
		{Kind: "group", Name: GroupAllUsers, Label: GroupAllUsers, People: []string{}, Everyone: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("approvers:\n got %+v\nwant %+v", got, want)
	}
}
