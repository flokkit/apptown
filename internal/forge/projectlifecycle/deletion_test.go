package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/dataprotection"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
)

// scriptedDeployments stands in for Delivery: the project's deployments in
// the order they were made, a take-down that removes one (or refuses the
// one named), and backups that are always removed.
type scriptedDeployments struct {
	mu     sync.Mutex
	names  []string
	asked  []string
	refuse string
	as     []access.Principal
	// ops, when set, takes deployments down as admitted operations that
	// end as end says (removing the deployment when they succeed).
	ops *operations.Service
	db  *database.DB
	end operations.OperationState
	// stays names a deployment whose take-down says it is done, but which
	// still stands.
	stays string
}

var errTakeDownRefused = errors.New("the final backup fell short")

func (s *scriptedDeployments) ForProject(context.Context, ids.ID) ([]*deployment.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*deployment.Deployment{}
	for _, n := range s.names {
		out = append(out, &deployment.Deployment{Name: n, Kind: deployment.Standing})
	}
	return out, nil
}

func (s *scriptedDeployments) TakeDownForDeletion(_ context.Context, project ids.ID, name string, binding access.Principal) (*operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, name)
	s.as = append(s.as, binding)
	if name == s.refuse {
		return nil, errTakeDownRefused
	}
	if s.ops != nil {
		return database.Transact(context.Background(), s.db, func(tx *database.Tx) (*operations.Operation, error) {
			return s.ops.AdmitIn(context.Background(), tx, operations.Spec{Kind: dataprotection.OperationTakeDown, ProjectID: project,
				Subject: "project:" + project.String() + "/deployment:" + name, Authority: binding, Input: map[string]any{"deployment": name}})
		})
	}
	if name != s.stays {
		s.names = slices.DeleteFunc(s.names, func(n string) bool { return n == name })
	}
	return nil, nil
}

// takeDownKind is a take-down operation that ends as the script says.
func (s *scriptedDeployments) takeDownKind() operations.Kind {
	return operations.Kind{Name: dataprotection.OperationTakeDown, Capability: access.CapDeploymentTeardown, Read: access.CapDeploymentRead,
		Execute: func(_ context.Context, op operations.Operation, _ *operations.StepLog) operations.Outcome {
			if s.end == operations.Succeeded {
				s.mu.Lock()
				s.names = slices.DeleteFunc(s.names, func(n string) bool { return n == op.Input["deployment"] })
				s.mu.Unlock()
			}
			return operations.Outcome{State: s.end, Detail: "the copy was not checked"}
		}}
}

func (s *scriptedDeployments) PurgeProjectBackups(context.Context, ids.ID, func(context.Context) error) (int, []string, error) {
	return 0, nil, nil
}
func (s *scriptedDeployments) PurgeOrphanBackups(context.Context, time.Time) (int, []string, error) {
	return 0, nil, nil
}

// allowAll is a caller who may do anything but what it denies, as boss.
type allowAll struct{ deny map[access.Capability]bool }

func (a allowAll) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if a.deny[p.Capability] {
		return access.Caller{}, &access.Denied{Code: "test", Reason: "denied " + string(p.Capability)}
	}
	return access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session", Credential: "session:1"}, Verified: true}, nil
}

type noQueue struct{}

func (noQueue) EnqueueCleanupIn(context.Context, *database.Tx, ids.ID, int) error    { return nil }
func (noQueue) LiveIn(context.Context, *database.Tx, ids.ID, int) (bool, error)      { return false, nil }
func (noQueue) EnqueuePassIn(context.Context, *database.Tx, ids.ID, time.Time) error { return nil }

type deletionWorld struct {
	t       *testing.T
	db      *database.DB
	d       *Deletion
	deps    *scriptedDeployments
	project ids.ID
	clock   time.Time
	blocks  int
}

// newDeletionWorld is a project with deployments made in the order given
// (main first, as a project's first deployment usually is).
func newDeletionWorld(t *testing.T, names ...string) *deletionWorld {
	t.Helper()
	db := databaseSuite.New(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &deletionWorld{t: t, db: db, deps: &scriptedDeployments{names: names}, project: pid, clock: time.Now()}
	admission := consoleAdmissionFunc(func(string, string) (func(), error) { w.blocks++; return func() { w.blocks-- }, nil })
	w.d = NewDeletion(New(db, Dependencies{Projects: project.New(db, nil)}, Config{DataDir: t.TempDir()}), DeletionDependencies{
		Sessions: admission, Records: w.deps, Data: w.deps, Queue: noQueue{}, Authority: allowAll{}})
	w.d.now = func() time.Time { return w.clock }
	return w
}

func (w *deletionWorld) lifecycle() project.Lifecycle {
	w.t.Helper()
	l, err := project.LifecycleIn(context.Background(), w.db, w.project)
	if err != nil {
		w.t.Fatal(err)
	}
	return l
}

func (w *deletionWorld) pass() {
	w.t.Helper()
	if _, err := w.d.Pass(context.Background(), "", nil); err != nil {
		w.t.Fatal(err)
	}
}

// A deletion marks the project deleting at once and takes nothing down in
// its grace period; then the pass takes the deployments down one at a time
// in creation order with main last, under the deletion's binding, and the
// project is deleted and kept once none is left.
//
// Kills: main taken down first; a take-down before the grace period; a
// take-down as the person rather than the binding; deleted with a
// deployment left.
func TestTheDeletionPassTakesMainDownLastThenKeepsTheProject(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main", "staging", "beta")
	st, err := w.d.Request(context.Background(), w.project)
	if err != nil || st.State != project.Deleting || w.blocks != 1 {
		t.Fatalf("request: %+v %v (blocks %d)", st, err, w.blocks)
	}
	w.pass()
	if len(w.deps.asked) != 0 {
		t.Fatalf("taken down within the grace period: %v", w.deps.asked)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if want := []string{"staging", "beta", "main"}; !slices.Equal(w.deps.asked, want) {
		t.Fatalf("asked %v, want %v", w.deps.asked, want)
	}
	for _, as := range w.deps.as {
		if as.Credential != identity.BindingCredential(st.BindingID) || as.Person != "boss" {
			t.Fatalf("a take-down ran as %+v, not the deletion's binding", as)
		}
	}
	l := w.lifecycle()
	if l.State != project.Deleted || !l.KeptUntil.Time.Equal(l.DeletedAt.Time.Add(DefaultKeptDays*24*time.Hour)) {
		t.Fatalf("after the pass: %+v", l)
	}
	if w.blocks != 1 {
		t.Fatalf("a deleted project's chats are not blocked: %d", w.blocks)
	}
}

// The grace period is measured from the request by the deletion's own
// clock, which also ends it: with that clock eleven minutes ahead of or
// behind the database's, a pass at once takes nothing down, and a pass
// once the grace is over by that clock takes the deployments down.
//
// Kills: the request's time taken from the database's clock while the
// deletion's clock judges the grace.
func TestTheGracePeriodRunsOnTheDeletionsClock(t *testing.T) {
	t.Parallel()
	for _, offset := range []time.Duration{DeletionGrace + time.Minute, -(DeletionGrace + time.Minute)} {
		t.Run(offset.String(), func(t *testing.T) {
			t.Parallel()
			w := newDeletionWorld(t, "main", "staging")
			w.clock = time.Now().Add(offset)
			if _, err := w.d.Request(context.Background(), w.project); err != nil {
				t.Fatal(err)
			}
			w.pass()
			if len(w.deps.asked) != 0 {
				t.Fatalf("taken down within the grace period: %v", w.deps.asked)
			}
			w.clock = w.clock.Add(DeletionGrace + time.Minute)
			w.pass()
			if want := []string{"staging", "main"}; !slices.Equal(w.deps.asked, want) {
				t.Fatalf("after the grace period: asked %v, want %v", w.deps.asked, want)
			}
		})
	}
}

// A take-down that is refused stalls the deletion: main is not asked, the
// project stays deleting, and the next pass does nothing until a person
// re-confirms; then it goes on under a new binding, the old one revoked.
//
// Kills: a refused take-down counted as gone; main taken down after a
// refusal; a stalled deletion retried blind.
func TestARefusedTakeDownStallsTheDeletionUntilReconfirmed(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main", "staging", "review")
	w.deps.refuse = "review"
	st, err := w.d.Request(context.Background(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	w.pass()
	if want := []string{"staging", "review"}; !slices.Equal(w.deps.asked, want) {
		t.Fatalf("asked %v, want %v", w.deps.asked, want)
	}
	l := w.lifecycle()
	if l.State != project.Deleting || !strings.Contains(l.Stalled, "review could not be taken down") {
		t.Fatalf("after a refusal: %+v", l)
	}
	w.deps.refuse = ""
	again, err := w.d.Request(context.Background(), w.project)
	if err != nil || again.Stalled != "" || again.BindingID == st.BindingID {
		t.Fatalf("re-confirm: %+v %v", again, err)
	}
	if old, _ := identity.BindingIn(context.Background(), w.db, st.BindingID); !old.Revoked {
		t.Fatal("the replaced binding still stands")
	}
	w.pass()
	if w.lifecycle().State != project.Deleted {
		t.Fatalf("after re-confirming: %+v", w.lifecycle())
	}
}

// A deletion can be cancelled only before its first take-down was
// admitted; cancelling revokes its binding and releases the chats.
//
// Kills: cancelling after deployments started going.
func TestADeletionIsCancelledOnlyBeforeItsFirstTakeDown(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main", "staging")
	st, err := w.d.Request(context.Background(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	back, err := w.d.Cancel(context.Background(), w.project)
	if err != nil || back.State != project.Active || w.blocks != 0 {
		t.Fatalf("cancel: %+v %v (blocks %d)", back, err, w.blocks)
	}
	if b, _ := identity.BindingIn(context.Background(), w.db, st.BindingID); !b.Revoked {
		t.Fatal("a cancelled deletion's binding still stands")
	}
	w.deps.refuse = "main"
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if _, err := w.d.Cancel(context.Background(), w.project); err == nil || !strings.Contains(err.Error(), "can no longer be cancelled") {
		t.Fatalf("a cancel after a take-down: %v", err)
	}
}

// A deleted project returns to active by Restore, with no deployments,
// until its first purge was admitted; Restore needs the delete capability
// and deployment:restore, and revokes the deletion's binding.
//
// Kills: restoring after a purge started; restoring without both grants.
func TestRestoreReturnsADeletedProjectUntilItsPurgeIsAdmitted(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main")
	st, err := w.d.Request(context.Background(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	w.d.deps.Authority = allowAll{deny: map[access.Capability]bool{access.CapDeploymentRestore: true}}
	if _, err := w.d.Restore(context.Background(), w.project); err == nil {
		t.Fatal("restored without deployment:restore")
	}
	w.d.deps.Authority = allowAll{}
	back, err := w.d.Restore(context.Background(), w.project)
	if err != nil || back.State != project.Active || w.blocks != 0 {
		t.Fatalf("restore: %+v %v", back, err)
	}
	if b, _ := identity.BindingIn(context.Background(), w.db, st.BindingID); !b.Revoked {
		t.Fatal("a restored project's binding still stands")
	}
	// Deleted again, with its purge admitted: restoring is closed.
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if err := w.db.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := project.LockedLifecycleIn(context.Background(), tx, w.project); err != nil {
			return err
		}
		return project.StartPurgeIn(context.Background(), tx, w.project, w.clock)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Restore(context.Background(), w.project); err == nil || !strings.Contains(err.Error(), "purge has started") {
		t.Fatalf("restore after the purge was admitted: %v", err)
	}
}

// A revoked deletion binding stalls the pass before any take-down.
//
// Kills: a deletion that goes on under withdrawn authority.
func TestARevokedDeletionBindingStopsThePass(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main", "staging")
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.RevokeBinding(context.Background(), w.project, "changed my mind"); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if len(w.deps.asked) != 0 || !strings.Contains(w.lifecycle().Stalled, "was revoked") {
		t.Fatalf("after revoking: asked %v, %+v", w.deps.asked, w.lifecycle())
	}
}

type liveEvals []ids.ID

func (l liveEvals) LiveEvalsFromIn(context.Context, *database.Tx, ids.ID) ([]ids.ID, error) {
	return l, nil
}

// An eval whose arms still work from the project's code is live work: the
// deletion is refused, naming it, and nothing is marked or held.
//
// Kills: a project deleted under an eval copying from it.
func TestALiveEvalFromTheProjectRefusesItsDeletion(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main")
	w.d.deps.Evals = liveEvals{idstest.N(3)}
	if _, err := w.d.Request(context.Background(), w.project); err == nil || !strings.Contains(err.Error(), "eval "+idstest.N(3).String()) {
		t.Fatalf("deletion with a live eval: %v", err)
	}
	if l := w.lifecycle(); l.State != project.Active || w.blocks != 0 {
		t.Fatalf("a refused deletion changed the project: %+v (blocks %d)", l, w.blocks)
	}
	w.d.deps.Evals = liveEvals{}
	if st, err := w.d.Request(context.Background(), w.project); err != nil || st.State != project.Deleting {
		t.Fatalf("deletion once the eval ended: %+v %v", st, err)
	}
}

// A take-down the deletion admitted as an operation is waited for; one that
// failed (or whose end is unknown) stalls the deletion, named, and is never
// read as gone: main is not asked. Re-confirming the deletion lets the pass
// try again: the earlier attempt stays as it ended, and the new decision's
// take-downs finish the deletion.
//
// Kills: a failed take-down counted as done; the pass moving to main while
// a take-down is live; a re-confirmed deletion stalled again by the attempt
// it was re-confirmed after.
func TestAFailedTakeDownOperationStallsTheDeletionUntilReconfirmed(t *testing.T) {
	t.Parallel()
	for _, end := range []operations.OperationState{operations.Failed, operations.Unknown} {
		t.Run(end.String(), func(t *testing.T) {
			t.Parallel()
			w := newDeletionWorld(t, "main", "staging")
			if err := jobs.Migrate(context.Background(), w.db.Pool()); err != nil {
				t.Fatal(err)
			}
			ids := identity.New(w.db, identity.Config{Projects: project.NewCatalogue(w.db)})
			ops := operations.New(w.db, allowAll{}, ids, nil)
			w.deps.ops, w.deps.db, w.deps.end = ops, w.db, end
			ops.Register(w.deps.takeDownKind())
			w.d = NewDeletion(w.d.lifecycle, DeletionDependencies{Sessions: w.d.deps.Sessions, Records: w.deps, Data: w.deps, Queue: noQueue{},
				Authority: allowAll{}, Operations: ops, Now: func() time.Time { return w.clock }})
			run := func() *operations.Operation {
				t.Helper()
				st, err := w.d.Status(context.Background(), w.project)
				if err != nil || st.TakeDown == nil {
					t.Fatalf("no take-down: %+v %v", st, err)
				}
				op, err := ops.RunHere(context.Background(), st.TakeDown.ID)
				if err != nil {
					t.Fatal(err)
				}
				return op
			}
			if _, err := w.d.Request(context.Background(), w.project); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(DeletionGrace + time.Minute)
			w.pass()
			w.pass() // the take-down is live: waited for
			if want := []string{"staging"}; !slices.Equal(w.deps.asked, want) {
				t.Fatalf("asked %v while a take-down was live", w.deps.asked)
			}
			first := run()
			w.pass()
			if l := w.lifecycle(); l.State != project.Deleting || !strings.Contains(l.Stalled, "ended "+end.String()) {
				t.Fatalf("after a take-down that ended %s: %+v", end, l)
			}
			if want := []string{"staging"}; !slices.Equal(w.deps.asked, want) {
				t.Fatalf("asked %v after a failed take-down", w.deps.asked)
			}
			// The cause put right, the deletion is asked for again.
			w.deps.end = operations.Succeeded
			if _, err := w.d.Request(context.Background(), w.project); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				w.pass()
				if l := w.lifecycle(); l.Stalled != "" {
					t.Fatalf("a re-confirmed deletion stalled again: %+v", l)
				}
				if op := run(); op.State != operations.Succeeded {
					t.Fatalf("a take-down after re-confirming: %s", op.State)
				}
			}
			w.pass()
			if l := w.lifecycle(); l.State != project.Deleted {
				t.Fatalf("after re-confirming: %+v", l)
			}
			if want := []string{"staging", "staging", "main"}; !slices.Equal(w.deps.asked, want) {
				t.Fatalf("asked %v", w.deps.asked)
			}
			if got, err := ops.ByID(context.Background(), first.ID); err != nil || got.State != end {
				t.Fatalf("the earlier attempt: %+v %v", got, err)
			}
		})
	}
}

// A deployment whose take-down said it was removed but which still stands
// stalls the deletion rather than being taken down again and again.
//
// Kills: a pass that loops on a deployment that does not go.
func TestADeploymentThatDoesNotGoStallsTheDeletion(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main", "staging")
	w.deps.stays = "staging"
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(DeletionGrace + time.Minute)
	w.pass()
	if l := w.lifecycle(); !strings.Contains(l.Stalled, "staging was reported taken down but still stands") || len(w.deps.asked) != 1 {
		t.Fatalf("after a take-down that did not take: %+v, asked %v", l, w.deps.asked)
	}
}

// A restart re-applies the chat block of every project being deleted or
// deleted before serving, and of no other.
//
// Kills: chats opened on a deleting project after a restart.
func TestARestartHoldsTheChatsOfProjectsBeingDeleted(t *testing.T) {
	t.Parallel()
	w := newDeletionWorld(t, "main")
	if _, err := w.d.Request(context.Background(), w.project); err != nil {
		t.Fatal(err)
	}
	other, err := database.Transact(context.Background(), w.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "q", Name: "Q"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	restarted := NewDeletion(w.d.lifecycle, DeletionDependencies{Sessions: consoleAdmissionFunc(func(_, id string) (func(), error) {
		held = append(held, id)
		return func() {}, nil
	}), Records: w.deps, Data: w.deps, Queue: noQueue{}, Authority: allowAll{}})
	if err := restarted.HoldBlocks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{fmt.Sprint(w.project)}; !slices.Equal(held, want) {
		t.Fatalf("held %v, want %v (not %d)", held, want, other)
	}
}
