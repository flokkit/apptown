package conformance

import (
	"database/sql/driver"
	"errors"
	"flokkit.io/forge/internal/ids"
	"slices"
	"time"

	"flokkit.io/forge/internal/enum"
	"flokkit.io/forge/internal/stamp"
)

// DispositionKind is a person's classification of an exception. It is not a
// review outcome: records are created or revoked, and expiry is an observation.
type DispositionKind uint8

const (
	AcceptedRisk DispositionKind = iota + 1
	FalsePositive
)

// dispositionKindCodec spells each DispositionKind in storage and JSON.
var dispositionKindCodec = enum.Set[DispositionKind]{
	Names:     []string{"accepted", "false_positive"},
	Invalid:   "invalid-disposition",
	Err:       ErrDispositionKind,
	ScanLabel: "database value",
}

var ErrDispositionKind = errors.New("status must be accepted or false_positive")

func (k DispositionKind) String() string                     { return dispositionKindCodec.String(k) }
func ParseDispositionKind(s string) (DispositionKind, error) { return dispositionKindCodec.Parse(s) }
func (k DispositionKind) valid() bool                        { return k == AcceptedRisk || k == FalsePositive }

func (k DispositionKind) Value() (driver.Value, error) { return dispositionKindCodec.Value(k) }
func (k *DispositionKind) Scan(value any) error        { return dispositionKindCodec.Scan(k, value) }
func (k DispositionKind) MarshalJSON() ([]byte, error) { return dispositionKindCodec.JSON(k) }
func (k *DispositionKind) UnmarshalJSON(data []byte) error {
	return dispositionKindCodec.ReadJSON(k, data)
}

// Disposition is the shared exception record for scanner findings and guidance.
// A saved review retains the record as it was when admitted, including its expiry.
type Disposition struct {
	ID            ids.ID          `json:"id"`
	ProjectID     ids.ID          `json:"-"`
	Fingerprint   string          `json:"fingerprint,omitempty"`
	Identifier    string          `json:"identifier,omitempty"`
	Package       string          `json:"package,omitempty"`
	Status        DispositionKind `json:"status"`
	Justification string          `json:"justification"`
	By            string          `json:"by"`
	// ByPrincipal is the decider's principal; zero when not resolved to a person.
	ByPrincipal ids.ID     `json:"byPrincipal,omitzero"`
	CreatedAt   string     `json:"createdAt"`
	ExpiresAt   stamp.Time `json:"expiresAt,omitzero"`
	// byVia is how the decider acted, copied into a review's snapshot.
	byVia string
}

func (d Disposition) ActiveAt(at time.Time) bool {
	return d.Status.valid() && (!d.ExpiresAt.Valid || d.ExpiresAt.Time.After(at))
}

// FindingSubject is the identity needed for exception matching, independent of
// a scanner's report format or the application's Finding representation.
type FindingSubject struct {
	Fingerprint string
	Identifiers []string
	Package     string
}

// MatchDisposition prefers an exact fingerprint across the whole set, then an
// identifier match narrowed by package when specified. The input must be the
// active set at admission or a saved review's snapshot; it is not re-expired here.
func MatchDisposition(subject FindingSubject, dispositions []Disposition) *Disposition {
	for i := range dispositions {
		d := &dispositions[i]
		if !d.Status.valid() {
			continue
		}
		if d.Fingerprint != "" && d.Fingerprint == subject.Fingerprint {
			return d
		}
	}
	for i := range dispositions {
		d := &dispositions[i]
		if !d.Status.valid() {
			continue
		}
		if d.Identifier != "" && slices.Contains(subject.Identifiers, d.Identifier) && (d.Package == "" || d.Package == subject.Package) {
			return d
		}
	}
	return nil
}

// DispositionRef is the covering decision recorded alongside an excepted verdict.
type DispositionRef struct {
	ID     ids.ID          `json:"id"`
	Status DispositionKind `json:"status"`
	By     string          `json:"by"`
}

func (d Disposition) Reference() DispositionRef {
	return DispositionRef{ID: d.ID, Status: d.Status, By: d.By}
}

// ExceptionFor matches the whole-check identity against its admitted snapshot.
func ExceptionFor(sc DeclaredCheck, ds []Disposition) (DispositionRef, bool) {
	if d := MatchDisposition(FindingSubject{Fingerprint: Fingerprint(sc, "")}, ds); d != nil {
		return d.Reference(), true
	}
	return DispositionRef{}, false
}
