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

// ScanObservationsIn is retained evidence, newest first. The caller authorizes
// the surrounding project read. A historical subject never becomes a new one.
func ScanObservationsIn(ctx context.Context, db database.Queryer, project ids.ID) ([]ScanObservation, error) {
	rows, err := conformancesql.New(db.SQL()).ProjectScanObservations(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]ScanObservation, 0, len(rows))
	for _, body := range rows {
		var o ScanObservation
		if err := json.Unmarshal(body, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}
