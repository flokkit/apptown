package deployment

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// Kind is what a deployment is: a standing deployment is a project's app
// standing under a name on the ladder; a preview is a change's worktree
// serving under the change's URL, outside the ladder, one per change; a
// restored copy is a production deployment's release started from one of
// its copies, behind AppTown's access gate, for a set time
// (the dataprotection package). A restored copy is shown only on its source's card.
type Kind uint8

const (
	Standing Kind = iota + 1
	preview
	Restored
)

// kindCodec spells each Kind in storage and JSON.
var kindCodec = enum.Set[Kind]{
	Names:     []string{"standing", "preview", "restored-copy"},
	Invalid:   "invalid-kind",
	Err:       errKind,
	ScanLabel: "value",
}

var errKind = errors.New("invalid deployment kind")

func (k Kind) String() string { return kindCodec.String(k) }

// Standing reports whether the deployment is a standing one: the only kind
// that is backed up, deployed to by name, torn down by hand or rebuilt at
// boot. Previews belong to their changes.
func (e *Deployment) Standing() bool { return e.Kind == Standing }

func (k Kind) Value() (driver.Value, error) { return kindCodec.Value(k) }

func (k *Kind) Scan(value any) error { return kindCodec.Scan(k, value) }

func (k Kind) MarshalJSON() ([]byte, error) { return kindCodec.JSON(k) }

func (k *Kind) UnmarshalJSON(data []byte) error { return kindCodec.ReadJSON(k, data) }
