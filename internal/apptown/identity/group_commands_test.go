package identity

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestGroupCommandNormalization(t *testing.T) {
	t.Parallel()
	s := New(nil, Config{OpenInstance: true})
	if err := s.SaveGroup(context.Background(), GroupChange{Name: "Bad Name"}, testOpenPrincipal("ops")); !errors.Is(err, ErrGroupName) {
		t.Fatalf("invalid group reached persistence: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SaveGroup(ctx, GroupChange{Name: "kids"}, testOpenPrincipal("ops")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled group save: %v", err)
	}
	in := GroupChange{Name: "kids", Description: "unchanged", Members: []string{"zoe", "Bad Name", "", "amy", "zoe"}}
	g, err := in.prepare()
	if err != nil || !reflect.DeepEqual(g.Members, []string{"zoe", "amy", "zoe"}) || g.Description != in.Description {
		t.Fatalf("normalization: %+v %v", g, err)
	}
	g.Members[0] = "changed"
	if in.Members[0] != "zoe" {
		t.Fatal("group shares its caller's member slice")
	}
}

func TestGroupCommandReplacesMembersAndRecordsInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{OpenInstance: true})
	testGrantRoot(t, s, "ops")
	in := GroupChange{Name: "kids", Description: "first", Members: []string{"zoe", "Bad Name", "amy", "zoe"}}
	if err := s.SaveGroup(ctx, in, admittedOpenPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	groups, err := s.Groups(ctx)
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Members, []string{"amy", "zoe"}) {
		t.Fatalf("membership set: %+v %v", groups, err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 3 || users[0].Username != "amy" || users[1].Username != "ops" || users[2].Username != "zoe" || users[0].HasPassword || users[2].HasPassword {
		t.Fatalf("member names were not recorded: %+v %v", users, err)
	}
	saved := ledger(t, s.db, "group.saved")
	if len(saved) != 1 {
		t.Fatalf("receipts: %+v", saved)
	}
	if payload := string(saved[0].Payload); payload != `{"members":["zoe","amy","zoe"]}` {
		t.Fatalf("receipt changed normalized input order or duplicates: %s", payload)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "kids", Description: "second", Members: []string{"zoe"}}, admittedOpenPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	groups, err = s.Groups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Description != "second" || !reflect.DeepEqual(groups[0].Members, []string{"zoe"}) {
		t.Fatalf("replacement: %+v %v", groups, err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "kids"}, admittedOpenPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	groups, err = s.Groups(ctx)
	if err != nil || len(groups) != 1 || len(groups[0].Members) != 0 {
		t.Fatalf("cleared group: %+v %v", groups, err)
	}
}
