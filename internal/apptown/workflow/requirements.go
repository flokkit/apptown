package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// MissingArtifact checks the declaration in execution order. Earlier outputs
// satisfy later requirements; a later output cannot satisfy an earlier step.
// Existing project evidence is read in one query, never as one query per step.
func (s runAdmissions) MissingArtifact(ctx context.Context, projectID ids.ID, spec *schema.Spec) (string, error) {
	if projectID.IsZero() || spec == nil {
		return "", fmt.Errorf("artifact requirements need project and definition")
	}
	var required []string
	produced, seen := map[string]bool{}, map[string]bool{}
	for _, stage := range spec.Stages {
		for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
			raw, err := json.Marshal(step.With)
			if err != nil {
				return "", err
			}
			var declaration struct {
				Requires []string `json:"requires"`
				Output   struct {
					Kind string `json:"kind"`
				} `json:"output"`
			}
			if err := json.Unmarshal(raw, &declaration); err != nil {
				return "", fmt.Errorf("step %q artifact requirements: %w", step.Name, err)
			}
			for _, kind := range declaration.Requires {
				if kind != "" && !produced[kind] && !seen[kind] {
					required = append(required, kind)
					seen[kind] = true
				}
			}
			if declaration.Output.Kind != "" {
				produced[declaration.Output.Kind] = true
			}
		}
	}
	if len(required) == 0 {
		return "", nil
	}
	found, err := workflowsql.New(s.records.db.SQL()).ExistingArtifactKinds(ctx, workflowsql.ExistingArtifactKindsParams{ProjectID: projectID, Kinds: required})
	if err != nil {
		return "", err
	}
	available := make(map[string]bool, len(found))
	for _, kind := range found {
		available[kind] = true
	}
	for _, kind := range required {
		if !available[kind] {
			return kind, nil
		}
	}
	return "", nil
}
