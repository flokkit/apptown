package workflowexec

import (
	"context"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/workflow"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
)

// observer describes step attempts as spans and duration samples. Without a
// tracer and meter it is a no-op; the executor never branches on it. Run,
// step and attempt ids are span attributes only; the histograms are labelled
// by step type and outcome, never by anything that grows with the estate.
type observer struct {
	tracer   trace.Tracer
	meter    metric.Meter
	duration metric.Float64Histogram
	queued   metric.Float64Histogram
	attempts metric.Int64Counter
	links    AttemptLinks
	clock    func() time.Time // AppTown's clock, which times an attempt; nil is the real one
}

func (o observer) now() time.Time { return clock.Or(o.clock)() }

func newObserver(tracer trace.Tracer, meter metric.Meter, links AttemptLinks, now func() time.Time) observer {
	if tracer == nil {
		tracer = nooptrace.NewTracerProvider().Tracer("forge/workflowexec")
	}
	if meter == nil {
		meter = noopmetric.NewMeterProvider().Meter("forge/workflowexec")
	}
	o := observer{tracer: tracer, meter: meter, links: links, clock: now}
	// Steps take seconds to many minutes; the SDK's default buckets stop at
	// ten seconds and would put every attempt in the last one.
	stepBuckets := metric.WithExplicitBucketBoundaries(1000, 5000, 15000, 30000, 60000, 120000, 300000, 600000, 1200000, 1800000)
	o.duration, _ = meter.Float64Histogram("apptown_step_duration", metric.WithUnit("ms"), stepBuckets, metric.WithDescription("running time of a step attempt, by step type and outcome"))
	// Queue waits are usually zero; a first boundary of one second would make
	// interpolation report a zero wait as nearly a second. Start at zero.
	queueBuckets := metric.WithExplicitBucketBoundaries(0, 100, 500, 1000, 5000, 15000, 60000, 300000, 900000)
	o.queued, _ = meter.Float64Histogram("apptown_step_queued", metric.WithUnit("ms"), queueBuckets, metric.WithDescription("time a step attempt waited for a slot, by step type"))
	// The duration histogram only sees attempts whose harness started; the
	// counter sees every settlement, including preflight failures and
	// unclaimed attempts, which are the ones worth counting.
	o.attempts, _ = meter.Int64Counter("apptown_step_attempts_total", metric.WithDescription("step attempts settled, by step type and outcome"))
	// Nothing is seeded at zero. A series born mid-window is the stack's
	// problem: Prometheus's created-timestamp zero ingestion writes it, and
	// stock/observability/README.md says so. Seeding here would cover one
	// closed counter and leave every histogram and open-label counter behind.
	return o
}

// stepAttempt is one attempt's span with its queued and running children.
type stepAttempt struct {
	o        observer
	ctx      context.Context
	span     trace.Span
	stepType string
	running  trace.Span
	started  time.Time
	runID    ids.ID
	stepID   ids.ID
	attempt  int64
	links    AttemptLinks
	done     sync.Once
}

// AttemptLinks lets another owner find a running attempt's span, so an
// inference request can link to the step it served. Nil means no links.
type AttemptLinks interface {
	Publish(runID ids.ID, stepID ids.ID, attempt int64, sc trace.SpanContext)
	Forget(runID ids.ID, stepID ids.ID, attempt int64)
}

// begin opens the attempt's span. There is no request context on the
// dispatcher's goroutine, so the span is a root; a step is where a trace of
// forge's own work begins.
func (o observer) begin(ctx context.Context, runID, stepID ids.ID, stepType, workflow string, projectID ids.ID) *stepAttempt {
	ctx, span := o.tracer.Start(ctx, "workflow.step", trace.WithAttributes(
		attribute.String("apptown.step.type", stepType), attribute.String("apptown.workflow", workflow),
		attribute.String("apptown.project", projectID.String()), attribute.String("apptown.run", runID.String()), attribute.String("apptown.step", stepID.String())))
	return &stepAttempt{o: o, ctx: ctx, span: span, stepType: stepType, runID: runID, stepID: stepID, links: o.links}
}

// claimed records the attempt number and publishes the span for linking.
func (s *stepAttempt) claimed(attempt int64) {
	if s == nil {
		return
	}
	s.attempt = attempt
	s.span.SetAttributes(attribute.Int64("apptown.attempt", attempt))
	if s.links != nil {
		s.links.Publish(s.runID, s.stepID, attempt, s.span.SpanContext())
	}
}

// queued records the wait for a slot as a child span over the measured
// interval, and the sample.
func (s *stepAttempt) queued(from time.Time, binding string) {
	if s == nil {
		return
	}
	_, span := s.o.tracer.Start(s.ctx, "queued", trace.WithTimestamp(from), trace.WithAttributes(attribute.String("apptown.binding", binding)))
	span.End()
	s.o.queued.Record(s.ctx, float64(s.o.now().Sub(from).Milliseconds()), metric.WithAttributes(attribute.String("step_type", s.stepType)))
}

// run opens the running child when the harness starts.
func (s *stepAttempt) run() {
	if s == nil {
		return
	}
	s.started = s.o.now()
	_, s.running = s.o.tracer.Start(s.ctx, "running")
}

// harness closes the running child with how the harness ended. This is not
// the step's outcome: a harness that returned cleanly can still fail the
// step at settlement, and a step can be settled without a harness at all.
func (s *stepAttempt) harness(res piResult) {
	if s == nil {
		return
	}
	outcome := attemptOutcome(res)
	if s.running != nil {
		s.running.SetAttributes(attribute.String("apptown.harness.outcome", outcome))
		if attemptFailed(res) {
			s.running.SetStatus(codes.Error, outcome)
		}
		s.running.End()
		s.running = nil
	}
	s.span.SetAttributes(attribute.String("apptown.harness.outcome", outcome))
}

// settle closes the attempt with the step's recorded status. Every exit
// settles exactly once: the normal path with the status it recorded, and a
// deferred call with "abandoned" catches any path that returned early.
func (s *stepAttempt) settle(status string) {
	if s == nil {
		return
	}
	s.done.Do(func() {
		if s.running != nil {
			s.running.End()
		}
		if !s.started.IsZero() {
			s.o.duration.Record(s.ctx, float64(s.o.now().Sub(s.started).Milliseconds()), metric.WithAttributes(
				attribute.String("step_type", s.stepType), attribute.String("outcome", status)))
		}
		s.o.attempts.Add(s.ctx, 1, metric.WithAttributes(attribute.String("step_type", s.stepType), attribute.String("outcome", status)))
		s.span.SetAttributes(attribute.String("apptown.step.outcome", status))
		if status != "pass" {
			s.span.SetStatus(codes.Error, status)
		}
		if s.links != nil && s.attempt != 0 {
			s.links.Forget(s.runID, s.stepID, s.attempt)
		}
		s.span.End()
	})
}

// attemptOutcome names how a harness invocation ended, for the span and the
// histogram label: a closed set, never the harness's words.
func attemptOutcome(res piResult) string {
	switch {
	case res.TimedOut:
		return "timed_out"
	case res.ProviderError != "":
		return "provider_error"
	case res.RunErr != nil:
		return "error"
	}
	return "ok"
}

func attemptFailed(res piResult) bool { return attemptOutcome(res) != "ok" }

// Settlement outcomes for a span. A span reports what was recorded, not what
// was intended: when the step's own settlement failed to persist, the span
// says so rather than repeating the status that was never written.
const (
	outcomeRework          = "rework"
	outcomeSettlementError = "settlement_error"
)

func settlementOutcome(status workflow.StepState, err error) string {
	if err != nil {
		return outcomeSettlementError
	}
	return status.String()
}

func reworkOutcome(err error) string {
	if err != nil {
		return outcomeSettlementError
	}
	return outcomeRework
}
