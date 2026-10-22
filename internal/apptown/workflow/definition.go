package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

func (run *Run) Definition() (*schema.Spec, error) {
	var spec schema.Spec
	if err := json.Unmarshal([]byte(run.DefinitionJSON), &spec); err != nil {
		return nil, fmt.Errorf("run %d has corrupt definition: %w", run.ID, err)
	}
	if spec.Role == "" {
		spec.Role = "working" // old ledgers predate the rule; they never fused either way now
	}
	return &spec, nil
}
