package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ScanSubject is exactly the software observed. Empty Commit or Digest on an
// errored observation means that its identity could not be established.
type ScanSubject struct {
	On           string `json:"on"`
	Commit       string `json:"commit,omitempty"`
	Tree         string `json:"tree,omitempty"`
	History      string `json:"history,omitempty"`
	DeploymentID ids.ID `json:"deploymentId,omitzero"`
	Deployment   string `json:"deployment,omitempty"`
	BundleID     ids.ID `json:"bundleId,omitzero"`
	Part         string `json:"part,omitempty"`
	Digest       string `json:"digest,omitempty"`
}

// ScanObservation is independent of disposable execution material. Configuration
// includes both the tool configuration and the governing step options.
type ScanObservation struct {
	ID               ids.ID          `json:"id"`
	ProjectID        ids.ID          `json:"projectId"`
	ChangeID         ids.ID          `json:"changeId,omitzero"`
	RunID            ids.ID          `json:"runId"`
	StepID           ids.ID          `json:"stepId"`
	Attempt          int64           `json:"attempt"`
	Subject          ScanSubject     `json:"subject"`
	ExpectedScanners []string        `json:"expectedScanners"`
	Scanner          string          `json:"scanner"`
	BindingVersion   int             `json:"bindingVersion"`
	ConfigHash       string          `json:"configHash"`
	Configuration    string          `json:"configuration"`
	StepConfig       json.RawMessage `json:"stepConfig"`
	ObservedAt       time.Time       `json:"observedAt"`
	Verdict          string          `json:"verdict"`
	Findings         []Finding       `json:"findings"`
	Reason           string          `json:"reason,omitempty"`
}

// RecordScanObservationIn participates in a scan's claimed attempt transaction.
// The caller owns its fence and rollback; no Change or Run foreign key governs
// retention. A retry of the same observation identity preserves the first answer.
func RecordScanObservationIn(ctx context.Context, tx *database.Tx, o ScanObservation) error {
	if o.ProjectID.IsZero() || o.StepID.IsZero() || o.Verdict == "" || o.Subject.On == "" {
		return fmt.Errorf("scan observation needs a project, step, subject and verdict")
	}
	if o.ID.IsZero() {
		o.ID = ids.New(o.ObservedAt)
	}
	if len(o.Configuration) == 0 {
		o.Configuration = `{}`
	}
	if o.ExpectedScanners == nil {
		o.ExpectedScanners = []string{}
	}
	if o.Findings == nil {
		o.Findings = []Finding{}
	}
	body, err := json.Marshal(o)
	if err != nil {
		return err
	}
	subject, err := json.Marshal(o.Subject)
	if err != nil {
		return err
	}
	return conformancesql.New(tx.SQL()).RecordScanObservation(ctx, conformancesql.RecordScanObservationParams{ID: o.ID, ProjectID: o.ProjectID, ChangeID: o.ChangeID, RunID: o.RunID, StepID: o.StepID, Attempt: o.Attempt, Subject: string(subject), Scanner: o.Scanner, ObservedAt: o.ObservedAt, Receipt: body})
}

// ScanHead carries selection facts without decoding retained receipts.
type ScanHead struct {
	BindingVersion int
	ConfigHash     string
	ID             ids.ID
	RunID          ids.ID
	Subject        ScanSubject
	Scanner        string
	ObservedAt     time.Time
}

// ScanObservationHeadsIn returns selection facts newest first. The surrounding
// owner authorizes the project read.
func ScanObservationHeadsIn(ctx context.Context, db database.Queryer, project ids.ID) ([]ScanHead, error) {
	rows, err := conformancesql.New(db.SQL()).ScanObservationHeads(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]ScanHead, 0, len(rows))
	for _, r := range rows {
		o := ScanHead{ID: r.ID, RunID: r.RunID, Scanner: r.Scanner, BindingVersion: int(r.BindingVersion), ConfigHash: r.ConfigHash, ObservedAt: r.ObservedAt}
		if err := json.Unmarshal([]byte(r.Subject), &o.Subject); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// ScanObservationsByIDIn loads only the requested project's selected receipts,
// ordered by observed_at DESC, id DESC. Empty selection reads no receipts.
func ScanObservationsByIDIn(ctx context.Context, db database.Queryer, project ids.ID, selected []ids.ID) ([]ScanObservation, error) {
	out := []ScanObservation{}
	if len(selected) == 0 {
		return out, nil
	}
	rows, err := conformancesql.New(db.SQL()).ScanObservationsByID(ctx, conformancesql.ScanObservationsByIDParams{ProjectID: project, Ids: selected})
	if err != nil {
		return nil, err
	}
	for _, body := range rows {
		var o ScanObservation
		if err := json.Unmarshal(body, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// PatchTime is one vulnerability a scan of a running app found, in one
// deployment part as one scanner reported it. FirstSeen is the first time any
// of the project's scans found its fingerprint; PatchedAt, when Patched, is
// the first usable (pass or fail) running scan of the same part and scanner
// after its latest sighting there. It is an observation duration: it includes
// the wait for a fix to be published, which is not recorded, and the time
// between scans.
type PatchTime struct {
	ProjectID   ids.ID
	FirstSeen   time.Time
	LastPresent time.Time
	Patched     bool
	PatchedAt   time.Time
}

// PatchTimesIn returns the units patched since the time given, and those still
// open that a running scan found since then, for the projects the calling
// owner has already authorized. One query covers every project.
func PatchTimesIn(ctx context.Context, db database.Queryer, projects []ids.ID, since time.Time) ([]PatchTime, error) {
	out := []PatchTime{}
	if len(projects) == 0 {
		return out, nil
	}
	rows, err := conformancesql.New(db.SQL()).PatchTimes(ctx, conformancesql.PatchTimesParams{ProjectIds: projects, Since: since})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, PatchTime{ProjectID: r.ProjectID, FirstSeen: r.FirstSeen, LastPresent: r.LastPresent, Patched: r.Patched, PatchedAt: r.PatchedAt})
	}
	return out, nil
}
