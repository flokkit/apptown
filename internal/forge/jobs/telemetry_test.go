package jobs_test

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/telemetry"
	"flokkit.io/forge/internal/telemetry/otlpfake"
)

// With a tracer, each job execution is a span named by AppTown operation
// it carries, not River's one worker kind.
func TestQueueDescribesEachJobAsASpanNamedByOperation(t *testing.T) {
	t.Parallel()
	receiver := otlpfake.New()
	defer receiver.Close()
	providers, err := telemetry.Start(t.Context(), telemetry.Config{Endpoint: receiver.Endpoint(), Insecure: true, ServiceName: "forge-test", MetricInterval: time.Hour, CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	db := databaseSuite.New(t)
	ran := make(chan struct{}, 1)
	q, err := jobs.New(jobs.Options{Tracer: providers.Tracer.Tracer("test")}, db.Pool(), map[string]jobs.Kind{"probe": {MaxAttempts: 1, Run: func(context.Context, string, map[string]any) (map[string]any, error) {
		ran <- struct{}{}
		return nil, nil
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(context.Background(), "probe", "k1", nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- q.Run(ctx) }()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("job did not run")
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := providers.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = providers.Close(context.Background())
	for _, s := range receiver.Spans() {
		if s.Name == "job.probe" && s.Attributes["apptown.job.operation"] == "probe" && s.Attributes["apptown.job.key"] == "k1" && s.Attributes["apptown.job.attempt"] == "1" {
			return
		}
	}
	t.Fatalf("no job span: %+v", receiver.Spans())
}
