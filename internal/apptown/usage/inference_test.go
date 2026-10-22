package usage

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRequestsLedger(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	r := NewRequests(db)
	a, err := r.Begin(context.Background(), Request{Kind: "step", Actor: "run/7", ProjectID: idstest.N(1), RunID: idstest.N(7), StepID: idstest.N(70), Attempt: 1, Provider: "openrouter", Model: "m"})
	if err != nil || a.IsZero() {
		t.Fatalf("begin: %v %d", err, a)
	}
	b, _ := r.Begin(context.Background(), Request{Kind: "chat", Actor: "bls", ProjectID: idstest.N(1), SessionID: "s1", Provider: "openrouter", Model: "m"})
	if err := r.Finish(context.Background(), a, 200, "ok", `{"observed":true,"input":10,"output":5}`); err != nil {
		t.Fatal(err)
	}
	n, err := r.MarkInterrupted(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("interrupted: %v %d", err, n)
	}
	var outcome string
	// sql-guarantee: The receipt's recorded outcome is the guarantee, and no owner read returns inference_requests rows; adding one would be test-only API.
	if err := db.QueryRow(`SELECT outcome FROM inference_requests WHERE id=?`, b).Scan(&outcome); err != nil || outcome != "interrupted_restart" {
		t.Fatalf("open receipt marked: %v %q", err, outcome)
	}
	// sql-guarantee: As above, for the finished receipt.
	if err := db.QueryRow(`SELECT outcome FROM inference_requests WHERE id=?`, a).Scan(&outcome); err != nil || outcome != "ok" {
		t.Fatalf("finished receipt untouched: %v %q", err, outcome)
	}
}

// A receipt records the actor its workload was admitted as, principal and
// way: a run started by alice keeps naming her after she is renamed and her
// old name is given to someone else.
func TestARequestRecordsTheAdmittedActor(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	people := identity.New(db, identity.Config{})
	if _, err := people.EnsureUser(ctx, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := people.PutGrant(ctx, identity.Spec{SubjectType: "user", Subject: "ops", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := people.EnsureUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	alice, err := identity.ActorIn(ctx, db, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := people.Rename(ctx, "alice", "alicia", access.Principal{Kind: "user", Name: "ops", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := people.EnsureUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	id, err := NewRequests(db).Begin(ctx, Request{Kind: "step", Actor: "alice", Who: access.Actor{Principal: alice.Principal, Name: "alice", Via: access.ViaRunStep}, ProjectID: idstest.N(1), RunID: idstest.N(7), Provider: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var principal, via string
	// sql-guarantee: The receipt's recorded actor is the guarantee, and no owner read returns inference_requests rows.
	if err := db.QueryRow(`SELECT actor_principal::text, actor_via FROM inference_requests WHERE id=?`, id).Scan(&principal, &via); err != nil || principal != alice.Principal.String() || via != access.ViaRunStep {
		t.Fatalf("the receipt names %s via %q: %v", principal, via, err)
	}
}
