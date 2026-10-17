package preview

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type previewSourceFunc func(context.Context, ids.ID) (Change, error)

func (f previewSourceFunc) Change(ctx context.Context, id ids.ID) (Change, error) {
	return f(ctx, id)
}

// Pin leaves the source as it was: these changes have no chat on a worker.
func (previewSourceFunc) Pin(_ context.Context, ch Change) (Change, error) {
	return ch, nil
}

type previewHostFunc func(context.Context, Change, bool) (Result, error)

func (f previewHostFunc) BootPreview(ctx context.Context, ch Change, restart bool) (Result, error) {
	return f(ctx, ch, restart)
}

func TestPreviewCommandAdmission(t *testing.T) {
	for _, scenario := range []string{"unconfigured", "denied", "cancelled", "wrong-change", "missing-project", "invalid-id"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(previewCaller())
			defer cancel()
			denied := errors.New("required receipt failed")
			ch := Change{ID: idstest.N(7), ProjectID: idstest.N(3), SourceRevision: "selected"}
			var runtime *Runtime
			authorityCalls := 0
			authority := previewSourceFunc(func(context.Context, ids.ID) (Change, error) {
				authorityCalls++
				if scenario == "denied" {
					return Change{}, denied
				}
				return ch, nil
			})
			deps := Dependencies{Authority: previewPermit{}, Sources: authority, Host: previewHostFunc(func(context.Context, Change, bool) (Result, error) {
				t.Fatal("rejected command reached host")
				return Result{}, nil
			})}
			switch scenario {
			case "unconfigured":
				deps.Authority = nil
			case "cancelled":
				cancel()
			case "wrong-change":
				ch.ID = idstest.N(8)
			case "missing-project":
				ch.ProjectID = ids.Nil
			case "invalid-id":
				ch.ID = ids.Nil
			}
			id := idstest.N(7)
			if scenario == "invalid-id" {
				id = ids.Nil
			}
			s := New(testDB(t), deps)
			runtime = &s.runtime
			out, err := s.RequestChangePreview(ctx, Selection{ChangeID: id, Restart: true})
			if err == nil || out != (Result{}) {
				t.Fatalf("admission: %+v %v", out, err)
			}
			if scenario == "denied" && !errors.Is(err, denied) {
				t.Fatal(err)
			}
			if scenario == "cancelled" && (!errors.Is(err, context.Canceled) || authorityCalls != 0) {
				t.Fatalf("cancellation: %v calls=%d", err, authorityCalls)
			}
			if runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{}).State != Cold {
				t.Fatal("rejected command changed runtime")
			}
		})
	}
}

func TestPreviewCommandSelectionResultAndFailure(t *testing.T) {
	t.Parallel()
	var runtime *Runtime
	selected := Change{ID: idstest.N(7), ProjectID: idstest.N(3), SourceRevision: "historical"}
	failed := errors.New("provider did not boot")
	calls := 0
	s := New(testDB(t), Dependencies{
		Authority: previewPermit{}, Sources: previewSourceFunc(func(_ context.Context, id ids.ID) (Change, error) {
			if id != selected.ID {
				t.Fatal(id)
			}
			return selected, nil
		}),
		Host: previewHostFunc(func(_ context.Context, ch Change, restart bool) (Result, error) {
			calls++
			if ch != selected || !restart || runtime.Inspect(context.Background(), ChangeKey(ch.ID), previewFacts{}).State != Starting {
				t.Fatalf("host selection/ownership: %+v restart=%t", ch, restart)
			}
			out := Result{ProjectID: idstest.N(999), URL: "http://preview", Revision: "resolved", HostPort: 1234}
			if calls == 1 {
				return out, failed
			}
			return out, nil
		}),
	})
	runtime = &s.runtime
	out, err := s.RequestChangePreview(previewCaller(), Selection{ChangeID: idstest.N(7), Restart: true})
	var execution *ExecutionError
	if !errors.Is(err, failed) || !errors.As(err, &execution) || out != (Result{}) {
		t.Fatalf("failure leaked success: %+v %v", out, err)
	}
	if got := runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{}); got.State != Broken || got.Reason != failed.Error() {
		t.Fatalf("settlement: %+v", got)
	}
	out, err = s.RequestChangePreview(previewCaller(), Selection{ChangeID: idstest.N(7), Restart: true})
	if err != nil || out != (Result{ProjectID: idstest.N(3), URL: "http://preview", Revision: "resolved", HostPort: 1234}) {
		t.Fatalf("retry: %+v %v", out, err)
	}
	if got := runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{URL: out.URL}); got.State != Live || got.Reason != "" {
		t.Fatalf("retry settlement: %+v", got)
	}
}

// Moved from the full App restart fixture: the checked request must acquire the
// existing owner's interval before its host can reap, including cancellation.
func TestCancelledPreviewRestartDoesNotReapAnotherBoot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(previewCaller())
	defer cancel()
	admitted := make(chan struct{})
	s := New(testDB(t), Dependencies{
		Authority: previewPermit{}, Sources: previewSourceFunc(func(context.Context, ids.ID) (Change, error) {
			close(admitted)
			return Change{ID: idstest.N(7), ProjectID: idstest.N(3)}, nil
		}),
		Host: previewHostFunc(func(context.Context, Change, bool) (Result, error) {
			t.Error("cancelled restart reached host")
			return Result{}, nil
		}),
	})
	runtime := &s.runtime
	owner, err := runtime.Begin(context.Background(), ChangeKey(idstest.N(7)))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Finish(nil)
	done := make(chan error, 1)
	go func() {
		_, err := s.RequestChangePreview(ctx, Selection{ChangeID: idstest.N(7), Restart: true})
		done <- err
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("authority did not run")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not finish")
	}
	if runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{}).State != Starting {
		t.Fatal("cancelled restart settled the owner's boot")
	}
}

func previewCaller() context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "alice"}, Verified: true})
}

type previewPermit struct{}

func (previewPermit) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	c, _ := access.CallerFrom(ctx)
	return c, nil
}
func (previewSourceFunc) Console(context.Context, string) (Console, error) {
	return Console{}, errors.New("unexpected console lookup")
}

// Permission and selected source are independent dependencies: failed required
// receipts must stop before runtime ownership or provider effects.
func TestPreviewPermissionBeforeRuntime(t *testing.T) {
	t.Parallel()
	denied := errors.New("decision receipt unavailable")
	var runtime *Runtime
	source := previewSourceFunc(func(context.Context, ids.ID) (Change, error) {
		return Change{ID: idstest.N(7), ProjectID: idstest.N(3)}, nil
	})
	s := New(testDB(t), Dependencies{Sources: source,
		Authority: previewPermissionFunc(func(_ context.Context, p access.Permission) (access.Caller, error) {
			if p.Capability != access.CapChangePreview || p.Resource.ChangeID != idstest.N(7) || p.Resource.ProjectID != idstest.N(3) || !p.Context.Consequential {
				t.Fatalf("permission lost selection: %+v", p)
			}
			return access.Caller{}, denied
		}), Host: previewHostFunc(func(context.Context, Change, bool) (Result, error) {
			t.Fatal("denied preview reached provider")
			return Result{}, nil
		}),
	})
	runtime = &s.runtime
	if _, err := s.RequestChangePreview(previewCaller(), Selection{ChangeID: idstest.N(7), Restart: true}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if got := runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{}); got.State != Cold {
		t.Fatalf("denied command took runtime: %+v", got)
	}
}

type previewPermissionFunc func(context.Context, access.Permission) (access.Caller, error)

func (f previewPermissionFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return f(ctx, p)
}

// previewFacts is an observation of where a preview is served, as given.
type previewFacts Observation

func (f previewFacts) ObservePreview(context.Context) Observation {
	return Observation(f)
}
