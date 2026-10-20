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
// job's execution is a span named by its operation. Nil means no spans.
type Options struct {
	Tracer trace.Tracer
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
		attribute.String("forge.job.operation", req.Operation), attribute.String("forge.job.key", req.Key),
		attribute.Int("forge.job.attempt", job.Attempt), attribute.Int64("forge.job.id", job.ID)))
	defer span.End()
	err := doInner(ctx)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
