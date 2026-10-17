package dataprotection

import (
	"database/sql/driver"
	"errors"

	"flokkit.io/forge/internal/enum"
	"flokkit.io/forge/internal/ids"
)

// What a copy and a restore can claim. A backup item says how it was
// copied, so a file copy of a running service is never mistaken for a
// consistent one; a restored item says what witnessed it, so files put in
// place are never reported as data the service loaded.

// CopyMethod is how an item of a set was copied: by the need's declared
// command (consistent, by that declaration), as files while nothing wrote
// to them (a take-down's stopped app), or as files of a running service,
// which may be inconsistent. Older sets do not say.
type CopyMethod uint8

const (
	CopyDeclared CopyMethod = iota + 1
	CopyFiles
	CopyLiveFiles
)

var copyMethodCodec = enum.Set[CopyMethod]{
	Names:     []string{"declared", "files", "live-files"},
	Invalid:   "invalid-copy-method",
	Err:       ErrCopyMethod,
	ScanLabel: "value",
}

var ErrCopyMethod = errors.New("invalid copy method")

func (m CopyMethod) String() string                   { return copyMethodCodec.String(m) }
func (m CopyMethod) Value() (driver.Value, error)     { return copyMethodCodec.Value(m) }
func (m *CopyMethod) Scan(value any) error            { return copyMethodCodec.Scan(m, value) }
func (m CopyMethod) MarshalJSON() ([]byte, error)     { return copyMethodCodec.JSON(m) }
func (m *CopyMethod) UnmarshalJSON(data []byte) error { return copyMethodCodec.ReadJSON(m, data) }

// RestoreOutcome is what one item's restore can claim:
//   - verified: the need's declared verify confirmed the service holds the copy;
//   - loaded: the need's declared restore ran in the running service, which
//     took the data (pg_restore exited 0);
//   - copied: files were put in place and nothing checked that anything
//     reads them (the tar fallback, the app's data volume, a set too old to
//     say how it was copied);
//   - failed: the restore or its verify did not succeed;
//   - skipped: the set holds no good copy of the item; it is left as it is.
type RestoreOutcome uint8

const (
	RestoreVerified RestoreOutcome = iota + 1
	RestoreLoaded
	RestoreCopied
	RestoreFailed
	RestoreSkipped
)

var restoreOutcomeCodec = enum.Set[RestoreOutcome]{
	Names:     []string{"verified", "loaded", "copied", "failed", "skipped"},
	Invalid:   "invalid-restore-outcome",
	Err:       ErrRestoreOutcome,
	ScanLabel: "value",
}

var ErrRestoreOutcome = errors.New("invalid restore outcome")

func (o RestoreOutcome) String() string               { return restoreOutcomeCodec.String(o) }
func (o RestoreOutcome) Value() (driver.Value, error) { return restoreOutcomeCodec.Value(o) }
func (o *RestoreOutcome) Scan(value any) error        { return restoreOutcomeCodec.Scan(o, value) }
func (o RestoreOutcome) MarshalJSON() ([]byte, error) { return restoreOutcomeCodec.JSON(o) }
func (o *RestoreOutcome) UnmarshalJSON(data []byte) error {
	return restoreOutcomeCodec.ReadJSON(o, data)
}

// RestoreStatus is a whole restore's outcome: restored when every item it
// restored was verified or loaded by its service; not verified when every
// item went back but some only as copied files; partly restored when an item
// failed.
type RestoreStatus uint8

const (
	RestoreDone RestoreStatus = iota + 1
	RestoreNotVerified
	RestorePartial
)

var restoreStatusCodec = enum.Set[RestoreStatus]{
	Names:     []string{"restored", "restored, not verified", "partly restored"},
	Invalid:   "invalid-restore-status",
	Err:       ErrRestoreStatus,
	ScanLabel: "value",
}

var ErrRestoreStatus = errors.New("invalid restore status")

func (s RestoreStatus) String() string                   { return restoreStatusCodec.String(s) }
func (s RestoreStatus) Value() (driver.Value, error)     { return restoreStatusCodec.Value(s) }
func (s *RestoreStatus) Scan(value any) error            { return restoreStatusCodec.Scan(s, value) }
func (s RestoreStatus) MarshalJSON() ([]byte, error)     { return restoreStatusCodec.JSON(s) }
func (s *RestoreStatus) UnmarshalJSON(data []byte) error { return restoreStatusCodec.ReadJSON(s, data) }

// restoreStatus is the rule from the items' outcomes.
func restoreStatus(items []RestoredItem) RestoreStatus {
	status := RestoreDone
	for _, it := range items {
		switch it.Outcome {
		case RestoreFailed:
			return RestorePartial
		case RestoreCopied:
			status = RestoreNotVerified
		}
	}
	return status
}

// RestoreResult is a restore's receipt, as the API returns it and the audit
// record keeps it.
type RestoreResult struct {
	Backup       ids.ID `json:"backup"`
	From         string `json:"from"`
	SafetyBackup ids.ID `json:"safetyBackup"`
	// Stopped and Started are whether the app's parts were stopped for the
	// restore and all started again, NotStarted the parts that would not
	// start; Ready whether the app answered its probe after.
	Stopped    bool           `json:"stopped"`
	Started    *bool          `json:"started,omitempty"`
	NotStarted []string       `json:"notStarted,omitempty"`
	Ready      *bool          `json:"ready,omitempty"`
	Status     RestoreStatus  `json:"status"`
	Items      []RestoredItem `json:"items"`
	Error      string         `json:"error,omitempty"`
}

// RestoredItem is one item of a restore and what it can claim.
type RestoredItem struct {
	Kind    string         `json:"kind"` // volume | need
	Name    string         `json:"name"`
	Outcome RestoreOutcome `json:"outcome"`
	// Detail is the error, why the item was skipped, or what was not checked.
	Detail string `json:"detail,omitempty"`
	// Caveat is what the copy put back does not hold, as the need's kind
	// declares (S3 object metadata): a known loss, not an uncertainty.
	Caveat string `json:"caveat,omitempty"`
}
