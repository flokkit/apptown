package briefs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type briefEvidence struct {
	mu     sync.Mutex
	in     workflow.ApprovalBriefInputs
	cache  map[string]string
	writes int
}

func (f *briefEvidence) Step(context.Context, ids.ID) (workflow.StepContext, error) {
	return f.in.StepContext, nil
}
func (f *briefEvidence) read(failure bool) workflow.ApprovalBriefInputs {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := f.in
	name := "step-" + in.Step.ID.String()
	if failure {
		name = fmt.Sprintf("step-%s-attempt-%d", in.Step.ID, in.Step.Attempt)
	}
	in.CacheName = name
	in.Cached = f.cache[name]
	return in
}
func (f *briefEvidence) ApprovalBrief(context.Context, ids.ID) (workflow.ApprovalBriefInputs, error) {
	return f.read(false), nil
}
func (f *briefEvidence) FailureAdvice(context.Context, ids.ID) (workflow.ApprovalBriefInputs, error) {
	return f.read(true), nil
}
func (f *briefEvidence) BriefDiff(context.Context, workflow.ApprovalBriefInputs) (string, string) {
	return "one file changed", "diff --git a/water.js b/water.js\n+ ripple(pointer.x, pointer.y)"
}
func (f *briefEvidence) SaveBrief(_ context.Context, _ ids.ID, _, name, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache[name] = body
	f.writes++
	return nil
}

type briefModels struct {
	slot    string
	pattern string
}

func (m briefModels) Resolve(_ context.Context, _ ids.ID, slot string) (string, string, string, error) {
	if m.slot != slot {
		return "", "", "", errors.New("wrong slot: " + slot)
	}
	return m.pattern, slot, "", nil
}

type briefCompletion struct {
	check func(modelcall.Call)
	calls atomic.Int32
	out   string
	err   error
}

func (c *briefCompletion) Ask(_ context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	c.calls.Add(1)
	if c.check != nil {
		c.check(call)
	}
	if call.Model != "strong" {
		return "", nil, errors.New("wrong model")
	}
	return c.out, nil, c.err
}
func evidenceForBriefs() *briefEvidence {
	return &briefEvidence{cache: map[string]string{}, in: workflow.ApprovalBriefInputs{StepContext: workflow.StepContext{Run: &workflow.Run{ID: idstest.N(1), ProjectID: idstest.N(2)}, Step: &workflow.Step{ID: idstest.N(3), Attempt: 1, Status: workflow.StatusFail}}, Acts: []workflow.NextAct{{Act: "retry"}}, Change: &workflow.Change{Title: "Fix links", State: workflow.ChangeInProgress}}}
}

// A completed persisted build is reused both within a flight and after it.
func TestConcurrentBriefReadsBuildAndPersistOnce(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "failure"}[failure], func(t *testing.T) {
			t.Parallel()
			f := evidenceForBriefs()
			slot := "main-strong"
			c := &briefCompletion{out: "retry\nThe check could not run. Try it again."}
			s := &Service{work: f, selection: briefModels{slot: slot, pattern: "strong"}, completion: c}
			read := func() {
				if failure {
					a, err := s.Failure(context.Background(), f.in.Step.ID)
					if err != nil || a.Recommend != "retry" {
						t.Errorf("advice %+v: %v", a, err)
					}
				} else {
					a, err := s.Approval(context.Background(), f.in.Step.ID)
					if err != nil || a == "" {
						t.Errorf("approval %q: %v", a, err)
					}
				}
			}
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(read)
			}
			wg.Wait()
			read()
			if c.calls.Load() != 1 || f.writes != 1 {
				t.Fatalf("calls=%d writes=%d", c.calls.Load(), f.writes)
			}
			if failure {
				f.in.Acts = nil
				a, err := s.Failure(context.Background(), f.in.Step.ID)
				if err != nil || a.Text == "" || a.Recommend != "" {
					t.Fatalf("stale act highlighted: %+v %v", a, err)
				}
				f.in.Step = &workflow.Step{ID: f.in.Step.ID, Attempt: 2, Status: workflow.StatusFail}
				f.in.Acts = []workflow.NextAct{{Act: "retry"}}
				read()
				if c.calls.Load() != 2 || f.writes != 2 {
					t.Fatal("new attempt reused old advice")
				}
			}
		})
	}
}
func TestAdviceAbsentWithoutStrongModelOrAfterFailedCall(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{"", "strong"} {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			f := evidenceForBriefs()
			c := &briefCompletion{err: errors.New("provider failed")}
			s := &Service{work: f, selection: briefModels{slot: "main-strong", pattern: pattern}, completion: c}
			a, err := s.Failure(context.Background(), f.in.Step.ID)
			if err != nil || a != (Advice{}) || f.writes != 0 {
				t.Fatalf("advice %+v, err %v writes %d", a, err, f.writes)
			}
			if pattern == "" && c.calls.Load() != 0 {
				t.Fatal("called a weaker model")
			}
		})
	}
}
func TestAdviceRecommendationRequiresAnExactOfferedAct(t *testing.T) {
	t.Parallel()
	for _, act := range []string{"send-back", "rework", "override", "retry"} {
		if a := parseAdvice(act+"\nThe check failed. Ask the agent to fix it.", []workflow.NextAct{{Act: workflow.NextActKind(act)}}); a.Recommend != act {
			t.Fatalf("offered %s rejected: %+v", act, a)
		}
	}

	for _, first := range []string{"retry", " retry ", "override", "Retry", "Act: retry", "`retry`", "send-back", "rework", "approve"} {
		a := parseAdvice(first+"\nThe check stopped. Try it again.", []workflow.NextAct{{Act: "retry"}})
		want := ""
		if first == "retry" || first == " retry " {
			want = "retry"
		}
		text := first + "\nThe check stopped. Try it again."
		switch first {
		case "retry", " retry ", "override", "send-back", "rework":
			text = "The check stopped. Try it again."
		}
		if a.Recommend != want || a.Text != text {
			t.Fatalf("%q: %+v", first, a)
		}
	}
}

func TestMalformedAdviceIsPersistedAndReused(t *testing.T) {
	t.Parallel()
	for _, out := range []string{"The check failed.", "Act: retry\nThe check failed.", "retry"} {
		f := evidenceForBriefs()
		c := &briefCompletion{out: out}
		s := &Service{work: f, selection: briefModels{slot: "main-strong", pattern: "strong"}, completion: c}
		for range 3 {
			a, err := s.Failure(t.Context(), f.in.Step.ID)
			if err != nil || a.Text != out || a.Recommend != "" {
				t.Fatalf("%+v: %v", a, err)
			}
		}
		if c.calls.Load() != 1 || f.writes != 1 {
			t.Fatalf("calls=%d writes=%d", c.calls.Load(), f.writes)
		}
	}
}
func TestFailedAdviceCoolsOnlyTheFailedAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := evidenceForBriefs()
	c := &briefCompletion{err: errors.New("outage")}
	s := &Service{work: f, selection: briefModels{slot: "main-strong", pattern: "strong"}, completion: c, now: func() time.Time { return now }}
	for range 3 {
		if _, err := s.Failure(t.Context(), f.in.Step.ID); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls.Load() != 1 {
		t.Fatalf("calls=%d", c.calls.Load())
	}
	f.in.Step.Attempt++
	if _, err := s.Failure(t.Context(), f.in.Step.ID); err != nil {
		t.Fatal(err)
	}
	if c.calls.Load() != 2 {
		t.Fatal("new attempt was cooled")
	}
	now = now.Add(10 * time.Minute)
	if _, err := s.Failure(t.Context(), f.in.Step.ID); err != nil {
		t.Fatal(err)
	}
	if c.calls.Load() != 3 {
		t.Fatal("expired cooling period prevented retry")
	}
}

// Only completed model prose may be saved. Missing models and failed answers
// leave no artifact, so a later open can build the brief.
func TestApprovalCachesOnlyModelProse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, pattern, out string
		err                error
		wantError          bool
	}{
		{name: "no model"},
		{name: "model error", pattern: "strong", err: errors.New("provider failed"), wantError: true},
		{name: "empty answer", pattern: "strong", out: " \n ", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := evidenceForBriefs()
			c := &briefCompletion{out: tc.out, err: tc.err}
			s := &Service{work: f, selection: briefModels{slot: "main-strong", pattern: tc.pattern}, completion: c}
			text, err := s.Approval(t.Context(), f.in.Step.ID)
			if text != "" || (err != nil) != tc.wantError || f.writes != 0 {
				t.Fatalf("brief=%q error=%v writes=%d", text, err, f.writes)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost provider error: %v", err)
			}
			c.err, c.out = nil, "Adds working links."
			s.selection = briefModels{slot: "main-strong", pattern: "strong"}
			text, err = s.Approval(t.Context(), f.in.Step.ID)
			if err != nil || text != c.out || f.writes != 1 {
				t.Fatalf("retry brief=%q error=%v writes=%d", text, err, f.writes)
			}
		})
	}
}
