package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RemediationCoverage identifies the check an open remediation change fixes.
// Coverage ends only when the change lands or is abandoned.
type RemediationCoverage struct {
	ProjectID ids.ID `json:"projectId"`
	ChangeID  ids.ID `json:"changeId"`
	Number    int64  `json:"number"`
	Block     string `json:"block"`
	Version   int    `json:"version"`
	Check     string `json:"check"`
}

func (c RemediationCoverage) Refusal() error {
	return fault.New(fault.Conflict, fmt.Sprintf("This finding is being fixed in change #%d. Open that change to follow its progress.", c.Number))
}

// OpenRemediationsIn reads coverage for the caller's authorized projects.
// Admission callers hold the project lock until their transaction commits.
func OpenRemediationsIn(ctx context.Context, q database.Queryer, projects []ids.ID) ([]RemediationCoverage, error) {
	rows, err := workflowsql.New(q.SQL()).OpenRemediationChecks(ctx, projects)
	if err != nil {
		return nil, err
	}
	out := make([]RemediationCoverage, 0, len(rows))
	for _, r := range rows {
		out = append(out, RemediationCoverage{ProjectID: r.ProjectID, ChangeID: r.ID, Number: r.Number, Block: r.Block, Version: int(r.Version), Check: r.CheckName})
	}
	return out, nil
}
