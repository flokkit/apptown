package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/operations/internal/operationssql"
)

// Job is the River operation every operation's job carries; its key is the
// operation's id.
const Job = "operation"

// Kind is one kind of operation, registered by its owner at construction.
type Kind struct {
	Name string // namespaced: "deployment.take-down"
	// Capability is needed to start it and to cancel it; it is the one
	// checked again before each effect. Read is needed to read it.
	Capability access.Capability
	Read       access.Capability
	// Cancellable says the kind honours a cancel request between safe steps
	// once it runs; CancelRefusal, when set, is why one that does not
	// refuses. A queued operation of any kind can be cancelled: nothing ran.
	Cancellable   bool
	CancelRefusal error
	// Execute runs the operation once and says how it ended. It calls
	// log.StillAllowed before each consequential effect.
	Execute func(ctx context.Context, op Operation, log *StepLog) Outcome
	// SettleIn, when set, writes the owner's own records about the end in
	// the settlement's transaction (also when a restart settles it unknown).
	SettleIn func(ctx context.Context, tx *database.Tx, op Operation, out Outcome) error
	// Interrupted, when set, says from the operation's recorded facts what
	// a stop of the forge left (a restore's stopped app): words the restart
	// repair adds to its unknown detail. It reads only op; it acts on nothing.
	Interrupted func(op Operation) string
}

// Outcome is how an execution ended. State is Succeeded, Failed, Unknown or
// Cancelled; anything else reads unknown.
type Outcome struct {
	State        OperationState
	Verification string
	Detail       string
	Result       map[string]any
}

// Spec is what an owner admits.
type Spec struct {
	Kind      string
	ProjectID ids.ID
	// Subject is the hold path the operation is about; Holds every path it
	// holds while live (Subject itself when empty).
	Subject   string
	Holds     []string
	Authority access.Principal
	Input     map[string]any
}

// Authorities resolves a captured principal's credential again and checks
// its current grants. Identity implements it.
type Authorities interface {
	RequireStill(ctx context.Context, captured access.Principal, p access.Permission) error
}

// Service is the ledger, its worker and its restart repair.
type Service struct {
	db       *database.DB
	authz    access.Authorizer
	still    Authorities
	mu       sync.RWMutex
	kinds    map[string]Kind
	interval time.Duration // RunHere's poll while another runner has it
	clock    func() time.Time
}

// New builds the ledger. authz checks reads and cancels as the caller;
// still checks a running operation's captured authority before effects;
// now is the forge's clock, which stamps every row and step (the real
// clock when nil).
func New(db *database.DB, authz access.Authorizer, still Authorities, now func() time.Time) *Service {
	return &Service{db: db, authz: authz, still: still, kinds: map[string]Kind{}, interval: time.Second, clock: now}
}

func (s *Service) now() time.Time { return clock.Or(s.clock)().UTC() }

// Register adds a kind. A kind registered twice, or without a name, an
// executor or a capability, is a programming error.
func (s *Service) Register(k Kind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k.Name == "" || k.Execute == nil || k.Capability == "" {
		panic(fmt.Sprintf("operations: invalid kind %q", k.Name))
	}
	if _, ok := s.kinds[k.Name]; ok {
		panic(fmt.Sprintf("operations: kind %q registered twice", k.Name))
	}
	if k.Read == "" {
		k.Read = access.CapProjectRead
	}
	s.kinds[k.Name] = k
}

func (s *Service) kind(name string) (Kind, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.kinds[name]
	return k, ok
}

// AdmitIn records a queued operation, its River job and its
// operation.started receipt in the caller's transaction: neither the row
// nor the job exists without the other. The caller holds its own row locks
// already; AdmitIn takes the admission lock and refuses a hold that meets a
// live operation's with HeldError.
func (s *Service) AdmitIn(ctx context.Context, tx *database.Tx, spec Spec) (*Operation, error) {
	k, ok := s.kind(spec.Kind)
	if !ok {
		return nil, fmt.Errorf("no operation kind %q", spec.Kind)
	}
	holds := spec.Holds
	if len(holds) == 0 && spec.Subject != "" {
		holds = []string{spec.Subject}
	}
	for _, h := range append([]string{spec.Subject}, holds...) {
		if !validPath(h) {
			return nil, fmt.Errorf("operation %s: %q is not a hold path", spec.Kind, h)
		}
	}
	auth := AuthorityOf(spec.Authority)
	if auth.PrincipalID.IsZero() && auth.Kind == "user" {
		person, err := identity.ActorIn(ctx, tx, auth.Name)
		if err != nil {
			return nil, err
		}
		auth.PrincipalID = person.Principal
	}
	if auth.Name == "" || auth.Credential == "" {
		// Without a credential reference nothing could be checked before
		// an effect, so nothing is admitted.
		return nil, fault.New(fault.Forbidden, "this request carries no credential an operation can be checked against later; sign in or use a personal token")
	}
	if holder, err := ConflictsIn(ctx, tx, holds...); err != nil {
		return nil, err
	} else if holder != nil {
		return nil, &HeldError{Holder: holder}
	}
	input := spec.Input
	if input == nil {
		input = map[string]any{}
	}
	enc := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err) // maps of plain values and a fixed struct
		}
		return raw
	}
	q := operationssql.New(tx.SQL())
	number, err := q.NextOperationNumber(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	r, err := q.InsertOperation(ctx, operationssql.InsertOperationParams{ID: ids.New(now), Number: number, Kind: k.Name, ProjectID: spec.ProjectID,
		Subject: sql.NullString{String: spec.Subject, Valid: true}, Holds: enc(holds), AskedBy: auth.Name, AskedByPrincipal: auth.PrincipalID, Authority: enc(auth), Input: enc(input), At: now})
	if err != nil {
		return nil, err
	}
	job, err := jobs.InsertOperationTx(ctx, tx.Native(), jobs.Request{Operation: Job, Key: r.ID.String()})
	if err != nil {
		return nil, fmt.Errorf("queueing operation %d: %w", r.ID, err)
	}
	if n, err := q.SetOperationJob(ctx, operationssql.SetOperationJobParams{ID: r.ID, JobID: job.Job.ID}); err != nil || n != 1 {
		return nil, fmt.Errorf("recording operation %d's job: %d %w", r.ID, n, err)
	}
	r.JobID = job.Job.ID
	if err := audit.At(now).RecordIn(ctx, tx, auth.Name, spec.ProjectID, "operation.started", spec.Subject, map[string]any{
		"operation": r.ID, "kind": k.Name, "holds": holds, "input": input, "credential": auth.Credential, "via": auth.Via}); err != nil {
		return nil, err
	}
	return record(r)
}

// Run is the worker: it claims a queued operation and runs it once. A
// delivery of one that is not queued (claimed already, ended, cancelled
// before it started) does nothing.
func (s *Service) Run(ctx context.Context, id ids.ID) (*Operation, error) {
	r, err := operationssql.New(s.db.SQL()).ClaimOperation(ctx, operationssql.ClaimOperationParams{ID: id, At: s.now()})
	if found, err := database.Lookup(err); err != nil {
		return nil, err
	} else if !found {
		return s.ByID(ctx, id)
	}
	op, err := record(r)
	if err != nil {
		return nil, err
	}
	return s.execute(ctx, op)
}

// JobHandler adapts Run to the queue: the job's receipt names the
// operation and how it ended; the operation's row is its record.
func (s *Service) JobHandler(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil || id.IsZero() {
		return nil, jobs.Permanent(fmt.Errorf("invalid operation job key %q", key))
	}
	op, err := s.Run(ctx, id)
	if err != nil {
		// Not retried: the row says how far it got, and a restart settles a
		// running one unknown.
		return nil, jobs.Permanent(err)
	}
	return map[string]any{"operation": id, "state": op.State.String()}, nil
}

// RunHere runs an admitted operation in the caller, when the caller claims
// it first; when the worker has it, RunHere waits for it to end. Either
// way it returns the ended operation (or ctx's error while waiting).
func (s *Service) RunHere(ctx context.Context, id ids.ID) (*Operation, error) {
	op, err := s.Run(ctx, id)
	for err == nil && op.State.Live() {
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-time.After(s.interval):
		}
		op, err = s.ByID(ctx, id)
	}
	return op, err
}

// execute runs a claimed operation and settles it. A panic, an
// unclassified state and a context that ended (a stop, the budget) read
// unknown.
func (s *Service) execute(ctx context.Context, op *Operation) (*Operation, error) {
	k, ok := s.kind(op.Kind)
	log := &StepLog{s: s, op: op, kind: k}
	var out Outcome
	if !ok {
		out = Outcome{State: Failed, Detail: fmt.Sprintf("the forge does not know how to run a %s", op.Kind)}
	} else {
		out = s.guarded(ctx, k, *op, log)
	}
	switch out.State {
	case Succeeded, Failed, Cancelled, Unknown:
	default:
		out = Outcome{State: Unknown, Detail: strings.TrimSpace("it ended without saying how. " + out.Detail), Result: out.Result}
	}
	if ctx.Err() != nil && out.State != Unknown {
		// What it last did was cut off; whether its effect ended is not known.
		out.State = Unknown
		out.Detail = strings.TrimSpace(out.Detail + " It was stopped (the forge stopped or its two-hour budget ran out) before it could be said how it ended; look before asking again.")
	}
	if log.lost != nil {
		out.Detail = strings.TrimSpace(out.Detail + " (some steps were not recorded as they happened: " + log.lost.Error() + ")")
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.settle(settle, op, out, Running); err != nil {
		return op, err
	}
	return op, nil
}

func (s *Service) guarded(ctx context.Context, k Kind, op Operation, log *StepLog) (out Outcome) {
	defer func() {
		if v := recover(); v != nil {
			logPanic(op.ID, v)
			out = Outcome{State: Unknown, Detail: fmt.Sprintf("it stopped on an internal error (%v); whether its last step finished is not known. Look before asking again.", v), Result: log.op.Result}
		}
	}()
	return k.Execute(ctx, op, log)
}

func logPanic(id ids.ID, v any) { log.Printf("operation %d panicked: %v\n%s", id, v, debug.Stack()) }

// settle ends an operation from state: its row, the kind's records and the
// operation.ended receipt in one transaction. A receipt that cannot be
// written fails the transition, so the operation keeps its holds.
func (s *Service) settle(ctx context.Context, op *Operation, out Outcome, from OperationState) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error { return s.settleIn(ctx, tx, op, out, from) })
}

func (s *Service) settleIn(ctx context.Context, tx *database.Tx, op *Operation, out Outcome, from OperationState) error {
	now := s.now()
	if cur := op.Current(); cur != nil {
		ended := now
		cur.Ended = &ended
		switch out.State {
		case Unknown:
			cur.Unknown = true
			if cur.Detail == "" {
				cur.Detail = "not known: the forge stopped or got no answer"
			}
		case Failed, Cancelled:
			cur.Failed = true
			if cur.Detail == "" {
				cur.Detail = out.Detail
			}
		}
	}
	result := map[string]any{}
	for k, v := range op.Result {
		result[k] = v
	}
	for k, v := range out.Result {
		result[k] = v
	}
	if out.Verification != "" {
		result["verification"] = out.Verification
	}
	steps, err := json.Marshal(op.Steps)
	if err != nil {
		return err
	}
	res, err := json.Marshal(result)
	if err != nil {
		return err
	}
	n, err := operationssql.New(tx.SQL()).FinishOperation(ctx, operationssql.FinishOperationParams{ID: op.ID, State: out.State.String(),
		Steps: steps, Result: res, Detail: out.Detail, FromState: from.String(), At: now})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("operation %d was no longer %s: %w", op.ID, from, errMoved)
	}
	op.State, op.Detail, op.Result, op.Verification = out.State, out.Detail, result, out.Verification
	if k, ok := s.kind(op.Kind); ok && k.SettleIn != nil {
		if err := k.SettleIn(ctx, tx, *op, out); err != nil {
			return fmt.Errorf("settling operation %d's records: %w", op.ID, err)
		}
	}
	return audit.At(now).RecordIn(ctx, tx, op.AskedBy, op.ProjectID, "operation.ended", op.Subject, map[string]any{
		"operation": op.ID, "kind": op.Kind, "state": out.State, "verification": out.Verification, "detail": out.Detail, "result": result})
}

// RestartRepair counts what RepairInterruptedIn settled and requeued.
type RestartRepair struct{ Settled, Requeued int }

// RepairInterruptedIn runs in exclusive startup, after jobs'
// RepairInterruptedIn and before the queue starts: a running operation
// reads unknown, with its open step unknown and its kind's SettleIn; a
// queued one whose job is gone gets a runnable job. Ended ones are never
// touched. Any failure fails the caller's transaction, which blocks a
// writable start.
func (s *Service) RepairInterruptedIn(ctx context.Context, tx *database.Tx) (RestartRepair, error) {
	var out RestartRepair
	q := operationssql.New(tx.SQL())
	running, err := q.OperationsInStateForUpdate(ctx, Running.String())
	if err != nil {
		return out, err
	}
	for _, r := range running {
		op, err := record(r)
		if err != nil {
			return out, err
		}
		detail := interruptedDetail(op)
		if k, ok := s.kind(op.Kind); ok && k.Interrupted != nil {
			if left := strings.TrimSpace(k.Interrupted(*op)); left != "" {
				detail += " " + left
			}
		}
		if err := s.settleIn(ctx, tx, op, Outcome{State: Unknown, Detail: detail, Result: map[string]any{"interrupted": true}}, Running); err != nil {
			return out, err
		}
		out.Settled++
	}
	queued, err := q.OperationsInStateForUpdate(ctx, Queued.String())
	if err != nil {
		return out, err
	}
	if len(queued) == 0 {
		return out, nil
	}
	keys, err := jobs.UnfinishedKeysIn(ctx, tx.Native(), Job)
	if err != nil {
		return out, err
	}
	waiting := map[string]bool{}
	for _, k := range keys {
		waiting[k] = true
	}
	for _, r := range queued {
		key := r.ID.String()
		if waiting[key] {
			continue
		}
		job, err := jobs.InsertOperationTx(ctx, tx.Native(), jobs.Request{Operation: Job, Key: key})
		if err != nil {
			return out, fmt.Errorf("requeueing operation %d: %w", r.ID, err)
		}
		if _, err := q.SetOperationJob(ctx, operationssql.SetOperationJobParams{ID: r.ID, JobID: job.Job.ID}); err != nil {
			return out, err
		}
		out.Requeued++
	}
	return out, nil
}

// interruptedDetail says what a running operation had confirmed when the
// forge stopped.
func interruptedDetail(op *Operation) string {
	var done []string
	in := ""
	for _, st := range op.Steps {
		switch {
		case st.Ended == nil:
			in = st.Name
		case !st.Failed && !st.Unknown:
			done = append(done, st.Name)
		}
	}
	detail := "the forge stopped while this ran; whether it finished is not known."
	if len(done) > 0 {
		detail += " Confirmed: " + strings.Join(done, "; ") + "."
	} else {
		detail += " No step was confirmed."
	}
	if in != "" {
		detail += " It was in: " + in + "."
	}
	return detail + " Look before asking again."
}

// ByID is one operation, unchecked: for owners and the worker.
func (s *Service) ByID(ctx context.Context, id ids.ID) (*Operation, error) {
	r, err := operationssql.New(s.db.SQL()).OperationByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, err
	}
	op, err := record(r)
	if err != nil {
		return nil, err
	}
	if k, ok := s.kind(op.Kind); ok {
		op.Cancellable = op.State == Queued || (k.Cancellable && op.State == Running)
	}
	return op, nil
}

// Get is one operation for a caller who may read it: the kind's read
// capability in its project. One the caller may not read is missing.
func (s *Service) Get(ctx context.Context, id ids.ID) (*Operation, error) {
	op, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	read := access.CapProjectRead
	if k, ok := s.kind(op.Kind); ok {
		read = k.Read
	}
	if err := s.require(ctx, op, read, "operations.read", false); err != nil {
		var denied *access.Denied
		if errors.As(err, &denied) {
			return nil, ErrMissing
		}
		return nil, err
	}
	return op, nil
}

// require asks the caller's authority for c in the operation's project.
// An operation outlives its project: once the project is gone (its row no
// longer reads), only instance administration (platform:operate) reads or
// steers its history; no former project role does. An agent's credential
// carries no administration, so an agent reads it only while its project
// stands. A project that cannot be read for another reason is that error,
// never a reason to fall back.
func (s *Service) require(ctx context.Context, op *Operation, c access.Capability, route string, consequential bool) error {
	if s.authz == nil {
		return access.ErrAuthorityUnavailable
	}
	ctxt := access.Context{Route: route, Consequential: consequential}
	_, err := s.authz.Require(ctx, access.Permission{Capability: c, Resource: projectResource(op.ProjectID), Context: ctxt})
	if errors.Is(err, sql.ErrNoRows) && !op.ProjectID.IsZero() {
		_, err = s.authz.Require(ctx, access.Permission{Capability: access.CapPlatformOperate, Resource: access.Resource{Type: "instance"}, Context: ctxt})
	}
	return err
}

func projectResource(projectID ids.ID) access.Resource {
	return access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}
}

// errMoved is a settlement that lost its row to another transition.
var errMoved = errors.New("its state moved")

// ErrNotCancellable refuses a cancel of a running operation whose kind is
// not stopped part way.
var ErrNotCancellable = fault.New(fault.Conflict, "this operation is not stopped part way once it has started")

// Cancel asks a live operation to stop, as a caller with its kind's
// capability. A queued one has done nothing, so it is cancelled at once; a
// running one records the request, and its kind stops at the next safe
// step. A kind that is not cancellable refuses.
func (s *Service) Cancel(ctx context.Context, id ids.ID) (*Operation, error) {
	op, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	k, ok := s.kind(op.Kind)
	if !ok {
		return nil, fmt.Errorf("no operation kind %q", op.Kind)
	}
	if err := s.require(ctx, op, k.Capability, "operations.cancel", true); err != nil {
		return nil, err
	}
	if op.State == Queued {
		// Nothing ran, so it ends at once, settled like any other end: its
		// row, the kind's records and the operation.ended receipt together,
		// guarded on its still being queued.
		err := s.settle(ctx, op, Outcome{State: Cancelled, Detail: "cancelled before it started; nothing ran"}, Queued)
		if err == nil {
			return s.ByID(ctx, id)
		}
		if !errors.Is(err, errMoved) {
			return nil, err
		}
		// The worker claimed it meanwhile: ask it to stop instead.
	}
	if !k.Cancellable {
		if k.CancelRefusal != nil {
			return nil, k.CancelRefusal
		}
		return nil, ErrNotCancellable
	}
	n, err := operationssql.New(s.db.SQL()).RequestOperationCancel(ctx, operationssql.RequestOperationCancelParams{ID: id, At: s.now()})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, fault.New(fault.Conflict, "the operation is not running")
	}
	return s.ByID(ctx, id)
}

// Dismiss puts away an ended operation from its asker's list.
func (s *Service) Dismiss(ctx context.Context, id ids.ID, who string) error {
	person, err := identity.ActorIn(ctx, s.db, who)
	if err != nil {
		return err
	}
	n, err := operationssql.New(s.db.SQL()).DismissOperation(ctx, operationssql.DismissOperationParams{ID: id, AskedBy: who, AskedByPrincipal: person.Principal, At: s.now()})
	if err != nil {
		return err
	}
	if n != 1 {
		return fault.New(fault.NotFound, "no ended operation of yours with that id")
	}
	return nil
}

// EndedFor is what has ended of the operations a person asked for and not
// yet put away: what waits on them.
func (s *Service) EndedFor(ctx context.Context, who string) ([]Operation, error) {
	person, err := identity.ActorIn(ctx, s.db, who)
	if err != nil {
		return nil, err
	}
	rows, err := operationssql.New(s.db.SQL()).EndedFor(ctx, operationssql.EndedForParams{AskedBy: who, AskedByPrincipal: person.Principal})
	if err != nil {
		return nil, err
	}
	return records(rows)
}

// OnSubject is the operations about a subject, newest first.
func (s *Service) OnSubject(ctx context.Context, subject string, limit int) ([]Operation, error) {
	rows, err := operationssql.New(s.db.SQL()).OperationsOnSubject(ctx, operationssql.OperationsOnSubjectParams{Subject: sql.NullString{String: subject, Valid: true}, RowLimit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return records(rows)
}

// LiveOn is the live operation about a subject, nil when none is.
func (s *Service) LiveOn(ctx context.Context, subject string) (*Operation, error) {
	r, err := operationssql.New(s.db.SQL()).LiveOnSubject(ctx, sql.NullString{String: subject, Valid: true})
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return record(r)
}

// LatestEnded is the newest ended operation of a kind about a subject, nil
// when there is none.
func (s *Service) LatestEnded(ctx context.Context, subject, kind string) (*Operation, error) {
	r, err := operationssql.New(s.db.SQL()).LatestEndedOfKind(ctx, operationssql.LatestEndedOfKindParams{Subject: sql.NullString{String: subject, Valid: true}, Kind: kind})
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return record(r)
}

// Allowed asks whether a captured principal (a scoped system binding, a
// credential an owner kept) may do c in the project now: the same check an
// operation asks before each effect, for an owner admitting work under it.
func (s *Service) Allowed(ctx context.Context, pr access.Principal, c access.Capability, projectID ids.ID, route string) error {
	if s.still == nil {
		return fmt.Errorf("%w: authority cannot be checked", ErrAuthorityWithdrawn)
	}
	if err := s.still.RequireStill(ctx, pr, access.Permission{Capability: c, Resource: projectResource(projectID), Context: access.Context{Route: route, Consequential: true}}); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityWithdrawn, err)
	}
	return nil
}

// UnderProject is the operations about a project or anything under it (its
// deployments), newest first.
func (s *Service) UnderProject(ctx context.Context, projectID ids.ID, limit int) ([]Operation, error) {
	rows, err := operationssql.New(s.db.SQL()).OperationsUnderProject(ctx, operationssql.OperationsUnderProjectParams{ProjectID: projectID, Root: sql.NullString{String: fmt.Sprintf("project:%d", projectID), Valid: true}, RowLimit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return records(rows)
}

// Running is every running operation, oldest first: what a forge restart
// would interrupt, for `forge executing`. Unchecked: the route asks for
// the instance-wide read first.
func (s *Service) Running(ctx context.Context) ([]Operation, error) {
	rows, err := operationssql.New(s.db.SQL()).RunningOperations(ctx)
	if err != nil {
		return nil, err
	}
	return records(rows)
}
