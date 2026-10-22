package operations

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/operations/internal/operationssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/enum"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
)

// OperationState is how an operation stands.
//   - queued: admitted, its job not yet claimed; nothing has run;
//   - running: claimed by a worker;
//   - succeeded: the stated command completed (see Verification);
//   - failed: a known failure, possibly after recorded partial effects;
//   - unknown: the outcome cannot be established (AppTown stopped, a
//     panic, an unclassified error, the budget ran out);
//   - cancelled: an acknowledged safe stop.
type OperationState uint8

const (
	Queued OperationState = iota + 1
	Running
	Succeeded
	Failed
	Unknown
	Cancelled
)

var stateCodec = enum.Set[OperationState]{
	Names:     []string{"queued", "running", "succeeded", "failed", "unknown", "cancelled"},
	Invalid:   "invalid-operation-state",
	Err:       ErrState,
	ScanLabel: "value",
}

// ErrState is a state the ledger does not know.
var ErrState = errors.New("invalid operation state")

func (s OperationState) String() string                   { return stateCodec.String(s) }
func (s OperationState) Value() (driver.Value, error)     { return stateCodec.Value(s) }
func (s *OperationState) Scan(value any) error            { return stateCodec.Scan(s, value) }
func (s OperationState) MarshalJSON() ([]byte, error)     { return stateCodec.JSON(s) }
func (s *OperationState) UnmarshalJSON(data []byte) error { return stateCodec.ReadJSON(s, data) }

// Live says the operation still holds its paths.
func (s OperationState) Live() bool { return s == Queued || s == Running }

// Verification is a separate fact from success: whether what the operation
// made was shown to work (a copy filled into a new place and checked).
const (
	Verified    = "verified"
	NotVerified = "not-verified"
)

// OperationStep is one step as it happened. Ended is nil while it runs. Failed says
// it did not succeed; Unknown says AppTown stopped in it, so whether it
// did is not known.
type OperationStep struct {
	Name    string     `json:"name"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
	Failed  bool       `json:"failed,omitempty"`
	Unknown bool       `json:"unknown,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	// Bytes is how much this step moved, where it counts it.
	Bytes int64 `json:"bytes,omitempty"`
}

// Authority is what the admission captured of its caller: the principal,
// its credential reference and its bounds. It holds no secret. It is the
// ceiling an effect is checked against, never authority of its own.
type Authority struct {
	// PrincipalID is the person who answers for the operation; Nil for
	// AppTown itself, or a caller not resolved to a person.
	PrincipalID  ids.ID   `json:"principal,omitzero"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Via          string   `json:"via,omitempty"`
	Instrument   string   `json:"instrument,omitempty"`
	Person       string   `json:"person,omitempty"`
	ProjectID    ids.ID   `json:"projectId,omitzero"`
	Role         string   `json:"role,omitempty"`
	Purpose      string   `json:"purpose,omitempty"`
	DelegationID ids.ID   `json:"delegationId,omitzero"`
	Scopes       []string `json:"scopes,omitempty"`
	// Credential names the credential behind the request: token:<name>,
	// session:<id>, oauth:<delegation id>, header:<user> for a request a
	// trusted proxy or open mode attributed, or system:<binding>.
	Credential string `json:"credential"`
}

// AuthorityOf captures a principal.
func AuthorityOf(pr access.Principal) Authority {
	return Authority{PrincipalID: pr.ID, Kind: pr.Kind, Name: pr.Name, Via: pr.Via, Instrument: pr.Instrument, Person: pr.Person, ProjectID: pr.ProjectID,
		Role: pr.Role, Purpose: pr.Purpose, DelegationID: pr.DelegationID, Scopes: append([]string(nil), pr.Scopes...), Credential: pr.Credential}
}

// Principal is the captured principal, to be resolved again.
func (a Authority) Principal() access.Principal {
	return access.Principal{ID: a.PrincipalID, Kind: a.Kind, Name: a.Name, Via: a.Via, Instrument: a.Instrument, Person: a.Person, ProjectID: a.ProjectID,
		Role: a.Role, Purpose: a.Purpose, DelegationID: a.DelegationID, Scopes: append([]string(nil), a.Scopes...), Credential: a.Credential}
}

// Actor is who answers for the operation as its admission captured them:
// their principal and way of acting, never the asker's name looked up
// again (a rename or a later holder of the name changes nothing).
func (a Authority) Actor() access.Actor {
	return a.Principal().Actor()
}

// System is AppTown acting for itself under a named binding (the
// scheduled copies, the restored-copy sweep).
func System(binding string) access.Principal {
	return access.Principal{Kind: "instrument", Name: SystemActor, Instrument: access.SystemInstrument, Via: "system", Credential: "system:" + binding}
}

// SystemActor is the name AppTown acts under for itself.
const SystemActor = "apptown-system"

// Operation is one row of the ledger.
type Operation struct {
	ID        ids.ID `json:"id"`
	Number    int64  `json:"number"` // within AppTown, as people name it ("operation 9")
	Kind      string `json:"kind"`
	ProjectID ids.ID `json:"projectId"`
	// Subject is the hold path the operation is about; "" for history
	// carried over without one.
	Subject string   `json:"subject,omitempty"`
	Holds   []string `json:"holds"`
	AskedBy string   `json:"askedBy"`
	// AskedVia is how the asker came in (token, session, oauth, system).
	AskedVia     string          `json:"askedVia,omitempty"`
	Authority    Authority       `json:"-"`
	Input        map[string]any  `json:"input"`
	State        OperationState  `json:"state"`
	Verification string          `json:"verification,omitempty"`
	Steps        []OperationStep `json:"steps"`
	// Result holds the owner's facts and links; Detail says how it ended
	// in words: what failed, what is left and where.
	Result          map[string]any `json:"result"`
	Detail          string         `json:"detail,omitempty"`
	CancelRequested bool           `json:"cancelRequested,omitempty"`
	Cancellable     bool           `json:"cancellable"`
	CreatedAt       stamp.Time     `json:"createdAt"`
	StartedAt       stamp.Time     `json:"startedAt,omitzero"`
	TouchedAt       stamp.Time     `json:"touchedAt"`
	FinishedAt      stamp.Time     `json:"finishedAt,omitzero"`
	DismissedAt     stamp.Time     `json:"dismissedAt,omitzero"`
	JobID           int64          `json:"-"`
}

// Current is the step running now, nil between steps and after the end.
func (o *Operation) Current() *OperationStep {
	if n := len(o.Steps); n > 0 && o.Steps[n-1].Ended == nil {
		return &o.Steps[n-1]
	}
	return nil
}

// URL is the operation's API address.
func (o *Operation) URL() string { return Path(o.ID) }

// Path is an operation's API address.
func Path(id ids.ID) string { return "/api/v1/operations/" + id.String() }

func record(r operationssql.Operation) (*Operation, error) {
	o := &Operation{ID: r.ID, Number: r.Number, Kind: r.Kind, ProjectID: r.ProjectID, Subject: r.Subject.String, AskedBy: r.AskedBy,
		Detail: r.Detail, CancelRequested: r.CancelRequested, JobID: r.JobID, CreatedAt: stamp.Of(r.CreatedAt), TouchedAt: stamp.Of(r.TouchedAt),
		StartedAt: stamp.Time{NullTime: r.StartedAt}, FinishedAt: stamp.Time{NullTime: r.FinishedAt}, DismissedAt: stamp.Time{NullTime: r.DismissedAt}}
	if err := o.State.Scan(r.State); err != nil {
		return nil, fmt.Errorf("operation %d: %w", r.ID, err)
	}
	for _, part := range []struct {
		raw  json.RawMessage
		into any
		what string
	}{{r.Holds, &o.Holds, "holds"}, {r.Authority, &o.Authority, "authority"}, {r.Input, &o.Input, "input"}, {r.Steps, &o.Steps, "steps"}, {r.Result, &o.Result, "result"}} {
		if err := json.Unmarshal(part.raw, part.into); err != nil {
			return nil, fmt.Errorf("operation %d %s: %w", r.ID, part.what, err)
		}
	}
	if o.Holds == nil {
		o.Holds = []string{}
	}
	if o.Steps == nil {
		o.Steps = []OperationStep{}
	}
	if o.Input == nil {
		o.Input = map[string]any{}
	}
	if o.Result == nil {
		o.Result = map[string]any{}
	}
	o.Verification, _ = o.Result["verification"].(string)
	o.AskedVia = o.Authority.Via
	return o, nil
}

func records(rows []operationssql.Operation) ([]Operation, error) {
	out := make([]Operation, 0, len(rows))
	for _, r := range rows {
		o, err := record(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, nil
}

// ErrMissing is an operation that does not exist, or that the caller may
// not read.
var ErrMissing = fault.New(fault.NotFound, "no such operation")

// HeldError refuses an admission whose holds meet a live operation's; it
// names the holder and links to it.
type HeldError struct{ Holder *Operation }

func (e *HeldError) Error() string {
	what := e.Holder.Subject
	if d, ok := e.Holder.Input["deployment"].(string); ok && d != "" {
		what = d
	}
	return fmt.Sprintf("%s is %s on %s (operation %d, asked by %s, %s)", e.Holder.Kind, e.Holder.State, what, e.Holder.ID, e.Holder.AskedBy, e.Holder.URL())
}
func (e *HeldError) FaultKind() fault.Kind { return fault.Conflict }

// LockIn takes the admission lock in the caller's transaction. An owner
// takes it after its own row locks and before its eligibility reads; AdmitIn
// and ConflictsIn take it too, so a caller that forgets still serialises.
func LockIn(ctx context.Context, tx *database.Tx) error {
	return operationssql.New(tx.SQL()).LockAdmission(ctx)
}

// ConflictsIn is the live operation holding any of paths, or nil. Release
// admission asks it under the project row lock, so a take-down and a
// release never both get in.
func ConflictsIn(ctx context.Context, tx *database.Tx, paths ...string) (*Operation, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if err := LockIn(ctx, tx); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		return nil, err
	}
	r, err := operationssql.New(tx.SQL()).LiveHolder(ctx, raw)
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return record(r)
}

// validPath is a hold path: segments separated by "/", none empty.
func validPath(p string) bool {
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			return false
		}
	}
	return true
}
