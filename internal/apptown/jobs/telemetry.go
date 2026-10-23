package jobs

import (
	"context"
	"encoding/json"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Options is what a queue may be given beyond its kinds: a tracer, so each
// job's execution is a span named by its operation (nil: no spans), and a
// hold, which a job waits on before its work starts while AppTown holds new
// work (a whole-AppTown backup's brief pause; nil: never held).
type Options struct {
	Tracer trace.Tracer
	Hold   func(context.Context) error
}

// jobHold is River worker middleware: a job's work waits while new work is
// held. Its context ending first ends the job as interrupted, never run.
type jobHold struct {
	river.MiddlewareDefaults
	hold func(context.Context) error
}

func (m *jobHold) Work(ctx context.Context, _ *rivertype.JobRow, doInner func(context.Context) error) error {
	if err := m.hold(ctx); err != nil {
		return err
	}
	return doInner(ctx)
}

// jobSpans is River worker middleware: one span per job execution, named by
// AppTown operation inside the job rather than River's single worker kind.
type jobSpans struct {
	river.MiddlewareDefaults
	tracer trace.Tracer
}

func (m *jobSpans) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	var req Request
	_ = json.Unmarshal(job.EncodedArgs, &req)
	name := "job." + req.Operation
	if req.Operation == "" {
		name = "job." + job.Kind
	}
	ctx, span := m.tracer.Start(ctx, name, trace.WithAttributes(
		attribute.String("apptown.job.operation", req.Operation), attribute.String("apptown.job.key", req.Key),
		attribute.Int("apptown.job.attempt", job.Attempt), attribute.Int64("apptown.job.id", job.ID)))
	defer span.End()
	err := doInner(ctx)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
