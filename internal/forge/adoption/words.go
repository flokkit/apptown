package adoption

import (
	"database/sql/driver"
	"errors"

	"flokkit.io/forge/internal/enum"
)

// AdoptionKind says how a project's adoption revision came to be: created
// with the project, edited by a person (guidance, lanes, a pack pulled),
// adopted from a prepared candidate (an uplift accepted), recorded as an
// uplift's target (what a change aims the project at; it is not the
// project's current revision until adopted), or snapshotted from history
// before revisions existed.
type AdoptionKind uint8

const (
	AdoptionCreate AdoptionKind = iota + 1
	AdoptionEdit
	AdoptionAdopt
	AdoptionTarget
	AdoptionLegacySnapshot
)

// adoptionKindCodec spells each AdoptionKind in storage and JSON.
var adoptionKindCodec = enum.Set[AdoptionKind]{
	Names:     []string{"create", "edit", "adopt", "target", "legacy-snapshot"},
	Invalid:   "invalid-adoption-kind",
	Err:       ErrAdoptionKind,
	ScanLabel: "value",
}

var ErrAdoptionKind = errors.New("invalid adoption kind")

func (k AdoptionKind) String() string { return adoptionKindCodec.String(k) }

// Standing reports whether a revision of this kind is one a project stands
// on: every kind but a target, which is what an uplift aims at until the
// project adopts it.
func (k AdoptionKind) Standing() bool { return k != AdoptionTarget && k.valid() }

func (k AdoptionKind) valid() bool { return k >= AdoptionCreate && k <= AdoptionLegacySnapshot }

func ParseAdoptionKind(text string) (AdoptionKind, error) { return adoptionKindCodec.Parse(text) }

func (k AdoptionKind) Value() (driver.Value, error) { return adoptionKindCodec.Value(k) }

func (k *AdoptionKind) Scan(value any) error { return adoptionKindCodec.Scan(k, value) }

func (k AdoptionKind) MarshalJSON() ([]byte, error) { return adoptionKindCodec.JSON(k) }

func (k *AdoptionKind) UnmarshalJSON(data []byte) error {
	return adoptionKindCodec.ReadJSON(k, data)
}
