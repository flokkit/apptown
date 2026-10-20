package workflowexec

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/telemetry"
	"flokkit.io/forge/internal/telemetry/otlpfake"
)

type linkKey struct {
	run, step ids.ID
	attempt   int64
}

type recordingLinks struct {
	published map[linkKey]trace.SpanContext
	forgotten int
}

func (r *recordingLinks) Publish(runID, stepID ids.ID, attempt int64, sc trace.SpanContext) {
	r.published[linkKey{runID, stepID, attempt}] = sc
}
func (r *recordingLinks) Forget(ids.ID, ids.ID, int64) { r.forgotten++ }

// A step attempt is one span with queued and running children in the same
// trace; the harness outcome and the step outcome are separate attributes;
// the attempt is published for linking while it runs and forgotten when it
// settles; the histograms carry step type and outcome only.
func TestObserverDescribesAStepAttempt(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	providers, err := telemetry.Start(t.Context(), telemetry.Config{Endpoint: receiver.Endpoint(), Insecure: true, ServiceName: "forge-test", MetricInterval: time.Hour, CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	links := &recordingLinks{published: map[linkKey]trace.SpanContext{}}
	var ticks atomic.Int64
	o := newObserver(providers.Tracer.Tracer("test"), providers.Meter.Meter("test"), links, func() time.Time {
		// Each read is five milliseconds on: the attempt's duration passes.
		return time.Unix(0, ticks.Add(int64(5*time.Millisecond)))
	})
	attempt := o.begin(t.Context(), idstest.N(7), idstest.N(42), "agent/pi", "quick-change", idstest.N(3))
	attempt.queued(time.Unix(0, 0).Add(-20*time.Millisecond), "paid")
	attempt.claimed(2)
	if sc, ok := links.published[linkKey{idstest.N(7), idstest.N(42), 2}]; !ok || !sc.IsValid() {
		t.Fatalf("attempt not published for linking: %+v", links.published)
	}
	attempt.run()
	attempt.harness(piResult{Text: "done"})
	attempt.settle("fail")      // the harness returned cleanly; settlement failed the step
	attempt.settle("abandoned") // the deferred safety net must not override
	if links.forgotten != 1 {
		t.Fatalf("forgotten %d times", links.forgotten)
	}
	if err := providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = providers.Close(context.Background())

	spans := receiver.Spans()
	byName := map[string]otlpfake.Span{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	step, ok := byName["workflow.step"]
	if !ok || byName["queued"].TraceID != step.TraceID || byName["running"].TraceID != step.TraceID || byName["running"].ParentID != step.SpanID {
		t.Fatalf("spans: %+v", spans)
	}
	a := step.Attributes
	if a["forge.step.type"] != "agent/pi" || a["forge.workflow"] != "quick-change" || a["forge.project"] != idstest.N(3).String() || a["forge.run"] != idstest.N(7).String() || a["forge.step"] != idstest.N(42).String() || a["forge.attempt"] != "2" ||
		a["forge.harness.outcome"] != "ok" || a["forge.step.outcome"] != "fail" {
		t.Fatalf("step attributes: %+v", a)
	}
	if byName["queued"].Attributes["forge.binding"] != "paid" || byName["running"].Attributes["forge.harness.outcome"] != "ok" {
		t.Fatalf("children: %+v %+v", byName["queued"].Attributes, byName["running"].Attributes)
	}
	var duration, queued, counted bool
	for _, m := range receiver.Metrics() {
		for k := range m.Attributes {
			if k != "step_type" && k != "outcome" {
				t.Fatalf("metric %s carries a label that grows with the estate: %s", m.Name, k)
			}
		}
		switch m.Name {
		case "apptown_step_duration":
			duration = duration || (m.Attributes["step_type"] == "agent/pi" && m.Attributes["outcome"] == "fail" && m.Value >= 5 && m.Value < 1000)
		case "apptown_step_queued":
			queued = queued || (m.Attributes["step_type"] == "agent/pi" && m.Value >= 20 && m.Value < 1000)
		case "apptown_step_attempts_total":
			counted = counted || (m.Attributes["step_type"] == "agent/pi" && m.Attributes["outcome"] == "fail" && m.Value == 1)
		}
	}
	if !duration || !queued || !counted {
		t.Fatalf("metrics: %+v", receiver.Metrics())
	}
}

// An attempt that returns before the harness runs still settles its span.
func TestObserverSettlesAnAttemptThatNeverRan(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	providers, err := telemetry.Start(t.Context(), telemetry.Config{Endpoint: receiver.Endpoint(), Insecure: true, ServiceName: "forge-test", MetricInterval: time.Hour, CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	o := newObserver(providers.Tracer.Tracer("test"), providers.Meter.Meter("test"), nil, nil)
	attempt := o.begin(t.Context(), idstest.N(1), idstest.N(2), "agent/review", "land", idstest.N(1))
	attempt.settle("error")
	_ = providers.Flush(context.Background())
	_ = providers.Close(context.Background())
	spans := receiver.Spans()
	if len(spans) != 1 || spans[0].Name != "workflow.step" || spans[0].Attributes["forge.step.outcome"] != "error" || spans[0].Attributes["forge.harness.outcome"] != "" {
		t.Fatalf("spans: %+v", spans)
	}
	// No harness ran, so no duration sample; the attempt is still counted.
	var counted bool
	for _, m := range receiver.Metrics() {
		if m.Name == "apptown_step_duration" {
			t.Fatalf("an attempt that never ran recorded a duration: %+v", m)
		}
		if m.Name == "apptown_step_attempts_total" && m.Attributes["outcome"] == "error" && m.Value == 1 {
			counted = true
		}
	}
	if !counted {
		t.Fatalf("attempt that never ran was not counted: %+v", receiver.Metrics())
	}
}

// Outcomes are a closed vocabulary derived from how the harness ended.
func TestAttemptOutcomeIsAClosedVocabulary(t *testing.T) {
	t.Parallel()
	for want, res := range map[string]piResult{
		"ok":             {},
		"timed_out":      {Outcome: execution.Outcome{TimedOut: true, RunErr: errors.New("killed")}},
		"provider_error": {ProviderError: "quota exhausted", Outcome: execution.Outcome{RunErr: errors.New("exit 1")}},
		"error":          {Outcome: execution.Outcome{RunErr: errors.New("exit 1")}},
	} {
		if got := attemptOutcome(res); got != want {
			t.Fatalf("%+v: got %q want %q", res, got, want)
		}
		if attemptFailed(res) != (want != "ok") {
			t.Fatalf("%q: failed flag wrong", want)
		}
	}
}

// A nil attempt, the no-op case, is safe to drive.
func TestObserverWithoutProvidersIsANoOp(t *testing.T) {
	t.Parallel()
	o := newObserver(nil, nil, nil, nil)
	attempt := o.begin(t.Context(), idstest.N(1), idstest.N(1), "agent/review", "land", idstest.N(1))
	attempt.queued(time.Now(), "x")
	attempt.claimed(1)
	attempt.run()
	attempt.harness(piResult{})
	attempt.settle("pass")
	var none *stepAttempt
	none.queued(time.Now(), "x")
	none.claimed(1)
	none.run()
	none.harness(piResult{})
	none.settle("pass")
}

// A span reports what settlement recorded: the status when it persisted, a
// distinct settlement error when it did not, and rework by name.
func TestSettlementOutcomeReportsWhatWasRecorded(t *testing.T) {
	t.Parallel()
	if got := settlementOutcome(workflow.StatusPass, nil); got != "pass" {
		t.Fatalf("pass: %q", got)
	}
	if got := settlementOutcome(workflow.StatusPass, errors.New("db down")); got != outcomeSettlementError {
		t.Fatalf("failed settlement reported %q", got)
	}
	if got := reworkOutcome(nil); got != outcomeRework {
		t.Fatalf("rework: %q", got)
	}
	if got := reworkOutcome(errors.New("db down")); got != outcomeSettlementError {
		t.Fatalf("failed rework settlement reported %q", got)
	}
}
