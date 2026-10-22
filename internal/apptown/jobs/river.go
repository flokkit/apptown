// Package jobs binds application operations to River's durable queue. River
// owns admission, claiming, retry timing and rescue; domain outcomes stay with
// the operation that owns them.
//
// RepairInterruptedIn is the sole exception to River-owned transitions: during
// exclusive startup, after the old daemon has exited and before consumers start,
// it settles dead-owner running jobs under their saved RetryOnInterrupt policy.
// It joins domain startup repair and its required audit receipts in one caller
// transaction. A foreign client ID alone never establishes that its owner died.
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"maps"
	"time"

	"github.com/flokkit/apptown/internal/apptown/jobs/internal/jobsql"
	"github.com/flokkit/apptown/internal/database"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

// Request keeps the operation and key as the deduplication identity. Changing
// inputs while the original request is open does not change that request.
type Request struct {
	Operation string         `json:"operation" river:"unique"`
	Key       string         `json:"key" river:"unique"`
	Values    map[string]any `json:"values"`
	// River increases MaxAttempts on an explicit retry. Keep interruption
	// policy with the request so that manual reruns cannot enable free retries.
	RetryOnInterrupt bool `json:"retryOnInterrupt"`
}

func (Request) Kind() string { return "forge.operation" }

// Enqueuer queues a job in the caller's transaction, so a domain change and
// the job that follows it commit together or not at all. The composition
// root supplies the one implementation: it puts the job on its kind's queue
// with its kind's attempt budget, and refuses a kind or key it does not know.
type Enqueuer interface {
	EnqueueIn(ctx context.Context, tx *database.Tx, r Request) error
}

func (r *Request) UnmarshalJSON(data []byte) error {
	type wire Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode((*wire)(r))
}

type Handler func(context.Context, string, map[string]any) (map[string]any, error)
type Kind struct {
	Run         Handler
	MaxAttempts int
	Timeout     time.Duration
	// Queue is the River queue the kind's jobs are inserted on: "" is the
	// default queue, OperationsQueue the long operations', ConformanceQueue
	// fleet reviews' and what-ifs', RemediationQueue remediation passes',
	// EvalsQueue eval passes'.
	Queue string
}

// The queues. Ordinary jobs run on the default queue, bounded by twenty
// minutes: our policy, not River's. Operations (internal/apptown/operations)
// run on their own queue, two at a time, bounded by two hours. Fleet
// reviews and what-ifs run on theirs, one at a time, bounded by two hours,
// so they never wait behind or hold up a take-down. Remediation passes run on
// theirs, one at a time, bounded by twenty minutes, so two passes never
// overlap. Eval passes run on theirs, one at a time, bounded by two hours,
// for the same reason. River rescues a job
// still running beyond OperationsRescue, so every budget stays below it
// (validated in New).
const (
	OperationsQueue   = "operations"
	ConformanceQueue  = "conformance"
	RemediationQueue  = "remediation"
	EvalsQueue        = "evals"
	DefaultTimeout    = 20 * time.Minute
	OperationsTimeout = 2 * time.Hour
	OperationsRescue  = OperationsTimeout + 10*time.Minute
)

// budget is the longest a kind's job may run on its queue; false for a
// queue AppTown does not run.
func budget(queue string) (time.Duration, bool) {
	switch queue {
	case "", river.QueueDefault, RemediationQueue:
		return DefaultTimeout, true
	case OperationsQueue, ConformanceQueue, EvalsQueue:
		return OperationsTimeout, true
	}
	return 0, false
}

// queueWorkers is how many jobs each queue runs at once. The remediation and
// evals queues run one: their passes rely on never overlapping.
var queueWorkers = map[string]int{river.QueueDefault: 4, OperationsQueue: 2, ConformanceQueue: 1, RemediationQueue: 1, EvalsQueue: 1}

func queueConfig() map[string]river.QueueConfig {
	queues := make(map[string]river.QueueConfig, len(queueWorkers))
	for name, n := range queueWorkers {
		queues[name] = river.QueueConfig{MaxWorkers: n}
	}
	return queues
}

type Reporter interface {
	Error(error)
	Panic(string, any, []byte)
}

type Schedule struct {
	Operation, Key string
	When           river.PeriodicSchedule
}

type Queue struct {
	Client *river.Client[*sql.Tx]
	kinds  map[string]Kind
}

func New(opts Options, db *sql.DB, kinds map[string]Kind, reporter Reporter, schedules ...Schedule) (*Queue, error) {
	kinds = maps.Clone(kinds)
	for name, k := range kinds {
		limit, ok := budget(k.Queue)
		if name == "" || k.Run == nil || k.MaxAttempts < 1 || k.Timeout < 0 || k.Timeout > limit || !ok || limit >= OperationsRescue {
			return nil, fmt.Errorf("invalid job configuration for %q", name)
		}
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, Worker(kinds))
	periodic := make([]*river.PeriodicJob, 0, len(schedules))
	for _, schedule := range schedules {
		kind, ok := kinds[schedule.Operation]
		if !ok || schedule.When == nil {
			return nil, fmt.Errorf("invalid schedule for %q", schedule.Operation)
		}
		periodic = append(periodic, river.NewPeriodicJob(schedule.When, func() (river.JobArgs, *river.InsertOpts) {
			return Request{Operation: schedule.Operation, Key: schedule.Key, RetryOnInterrupt: kind.MaxAttempts > 1}, insertOptions(kind.Queue, kind.MaxAttempts, time.Time{})
		}, &river.PeriodicJobOpts{ID: schedule.Operation + "/" + schedule.Key}))
	}
	client, err := river.NewClient(riverdatabasesql.New(db), &river.Config{
		Workers: workers, Queues: queueConfig(),
		PollOnly: true, MaxAttempts: 1, JobTimeout: DefaultTimeout, RescueStuckJobsAfter: OperationsRescue,
		PeriodicJobs: periodic, ErrorHandler: errorReporter{reporter},
		Logger:     slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn})),
		Middleware: middleware(opts),
	})
	if err != nil {
		return nil, err
	}
	return &Queue{Client: client, kinds: kinds}, nil
}

func (q *Queue) Enqueue(ctx context.Context, operation, key string, values map[string]any, after time.Time) (int64, error) {
	if key == "" {
		return 0, errors.New("job requires a key")
	}
	kind, ok := q.kinds[operation]
	if !ok {
		return 0, fmt.Errorf("no job operation %q", operation)
	}
	result, err := q.Client.Insert(ctx, Request{Operation: operation, Key: key, Values: values, RetryOnInterrupt: kind.MaxAttempts > 1}, insertOptions(kind.Queue, kind.MaxAttempts, after))
	if err != nil {
		return 0, err
	}
	return result.Job.ID, nil
}

// Producer provides River's transactional API without starting workers or
// owning another connection pool. A nil pool is valid for transaction-only use.
func Producer(db *sql.DB) (*river.Client[*sql.Tx], error) {
	return river.NewClient(riverdatabasesql.New(db), &river.Config{MaxAttempts: 1})
}

// InsertOnTx inserts a job in the caller's transaction onto the named queue
// ("" is the default), which must be the queue the kind is registered on.
func InsertOnTx(ctx context.Context, tx *sql.Tx, queue string, request Request, maxAttempts int, after time.Time) (*rivertype.JobInsertResult, error) {
	if _, ok := budget(queue); !ok {
		return nil, fmt.Errorf("no job queue %q", queue)
	}
	return insertTx(ctx, tx, queue, request, maxAttempts, after)
}

// InsertOperationTx inserts an operation's job on the operations queue in
// the caller's transaction: one attempt, never retried on interruption.
func InsertOperationTx(ctx context.Context, tx *sql.Tx, request Request) (*rivertype.JobInsertResult, error) {
	return insertTx(ctx, tx, OperationsQueue, request, 1, time.Time{})
}

func insertTx(ctx context.Context, tx *sql.Tx, queue string, request Request, maxAttempts int, after time.Time) (*rivertype.JobInsertResult, error) {
	if request.Operation == "" || request.Key == "" || maxAttempts < 1 {
		return nil, errors.New("job requires an operation, key and positive attempt limit")
	}
	request.RetryOnInterrupt = maxAttempts > 1
	client, err := Producer(nil)
	if err != nil {
		return nil, err
	}
	// River's single-insert helper indexes the first result without checking
	// for a silently suppressed write. Require the durable receipt ourselves.
	results, err := client.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: request, InsertOpts: insertOptions(queue, maxAttempts, after)}})
	if err != nil {
		return nil, err
	}
	if len(results) != 1 || results[0] == nil || results[0].Job == nil || results[0].Job.ID <= 0 {
		return nil, errors.New("job insertion did not return one durable receipt")
	}
	return results[0], nil
}

func insertOptions(queue string, maxAttempts int, after time.Time) *river.InsertOpts {
	return &river.InsertOpts{Queue: queue, MaxAttempts: maxAttempts, ScheduledAt: after, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending,
			rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled},
	}}
}

func Migrate(ctx context.Context, db *sql.DB) error {
	m, err := rivermigrate.New(riverdatabasesql.New(db), &rivermigrate.Config{Logger: slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn}))})
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// Run reports startup failure to the daemon. Its outer shutdown deadline still
// bounds process exit if a handler ignores cancellation or the database hangs.
func (q *Queue) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if err := q.Client.Start(ctx); err != nil {
		return fmt.Errorf("starting jobs: %w", err)
	}
	<-ctx.Done()
	// Stopping runs after ctx is done.
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return q.Client.StopAndCancel(stop)
}

type operationWorker struct {
	river.WorkerDefaults[Request]
	kinds map[string]Kind
}

// Worker binds the immutable operation registry for a River consumer. Separate
// consumers can use the same binding while River owns which jobs they claim.
func Worker(kinds map[string]Kind) river.Worker[Request] {
	return &operationWorker{kinds: maps.Clone(kinds)}
}

func (w *operationWorker) Timeout(job *river.Job[Request]) time.Duration {
	if timeout := w.kinds[job.Args.Operation].Timeout; timeout > 0 {
		return timeout
	}
	return 10 * time.Minute
}

func (w *operationWorker) Work(ctx context.Context, job *river.Job[Request]) error {
	kind, ok := w.kinds[job.Args.Operation]
	if !ok {
		return river.JobCancel(fmt.Errorf("unknown operation %q", job.Args.Operation))
	}
	receipt, err := kind.Run(ctx, job.Args.Key, job.Args.Values)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if IsPermanent(err) || (!job.Args.RetryOnInterrupt && ctx.Err() != nil) {
			// Wrapping context.Canceled would trigger River's shutdown attempt
			// refund even inside JobCancel. Keep text, not that sentinel cause.
			return river.JobCancel(fmt.Errorf("explicit rerun required: %v", err))
		}
		return err
	}
	return river.RecordOutput(ctx, receipt)
}

type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// Permanent marks err as a failure another attempt cannot fix.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type errorReporter struct{ Reporter }

func (r errorReporter) HandleError(_ context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	if r.Reporter != nil {
		r.Error(fmt.Errorf("job %d: %w", job.ID, err))
	}
	return nil
}
func (r errorReporter) HandlePanic(_ context.Context, job *rivertype.JobRow, value any, trace string) *river.ErrorHandlerResult {
	if r.Reporter != nil {
		r.Panic(fmt.Sprintf("job %d", job.ID), value, []byte(trace))
	}
	return nil
}

// UnfinishedKeysIn lets startup composition retain work owned by durable jobs.
// Queue state vocabulary and River SQL stay in this package.
func UnfinishedKeysIn(ctx context.Context, tx *sql.Tx, operation string) ([]string, error) {
	return jobsql.New(tx).UnfinishedKeys(ctx, operation)
}

// IncompleteKeysIn lists the keys of operation's jobs that have not
// completed: queued, running, retrying, or given up (cancelled or discarded).
// Where the job is the only record of an effect, only completion is its
// receipt.
func IncompleteKeysIn(ctx context.Context, tx *sql.Tx, operation string) ([]string, error) {
	return jobsql.New(tx).IncompleteKeys(ctx, operation)
}

func middleware(opts Options) []rivertype.Middleware {
	if opts.Tracer == nil {
		return nil
	}
	return []rivertype.Middleware{&jobSpans{tracer: opts.Tracer}}
}
