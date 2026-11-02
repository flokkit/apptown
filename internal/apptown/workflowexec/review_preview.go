package workflowexec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// previewEvidence is shared by both review runners. Files are supplied outside
// the app's mutable tree so app files cannot replace AppTown's pictures.
func (a *Executor) previewEvidence(ctx context.Context, run *workflow.Run, steps []*workflow.Step, reviewed string) (string, []execution.File, error) {
	var latest *workflow.Step
	for _, step := range steps {
		if step.Type == "preview" && step.Status != workflow.StatusPending && step.Status != workflow.StatusRunning && (latest == nil || step.ID.Compare(latest.ID) > 0) {
			latest = step
		}
	}
	if latest == nil {
		return "", nil, nil
	}
	var ev struct {
		Screenshots *struct {
			Commit string `json:"commit"`
			Shots  []struct {
				Artifact ids.ID `json:"artifact"`
				Path     string `json:"path"`
				View     string `json:"view"`
			} `json:"shots"`
			FailedResourcesMore int                           `json:"failedResourcesMore"`
			FailedResources     []verification.FailedResource `json:"failedResources"`
			Missing             []string                      `json:"missing"`
			Unavailable         string                        `json:"unavailable"`
		} `json:"screenshots"`
	}
	if err := json.Unmarshal(latest.Evidence, &ev); err != nil {
		return "", nil, fmt.Errorf("reading preview evidence: %w", err)
	}
	note := "\nAPPTOWN PREVIEW EVIDENCE:\nJudge how the page looks from AppTown's screenshots and failed-resource list, not from raw HTML. Open the PNGs with your image-reading tool. If pictures are unavailable or cannot be viewed, say that appearance could not be assessed.\n"
	if ev.Screenshots == nil {
		return note + "No screenshots were recorded.\n", nil, nil
	}
	shots := ev.Screenshots
	note += "Preview commit: " + strs.FirstNonBlank(shots.Commit, "unknown") + ".\nCommit under review: " + strs.FirstNonBlank(reviewed, "unknown") + ".\n"
	if shots.Commit == "" || reviewed == "" {
		note += "The screenshot commit could not be compared with the commit under review.\n"
	} else if shots.Commit != reviewed {
		note += "The pictures may be stale: these commits differ.\n"
	}
	var files []execution.File
	for i, shot := range shots.Shots {
		body, err := workflow.ReadRunBinaryArtifactIn(ctx, a.db, workflow.RunRef{ID: latest.RunID, ProjectID: run.ProjectID}, shot.Artifact, screenshotKind)
		if err != nil {
			return "", nil, fmt.Errorf("reading preview screenshot: %w", err)
		}
		at := fmt.Sprintf("%s/preview/%d.png", execution.InputsRoot, i+1)
		files = append(files, execution.File{Path: at, Content: body, Mode: 0o644})
		note += fmt.Sprintf("- %s · %s: %s\n", shot.Path, shot.View, at)
	}
	if shots.Unavailable != "" {
		note += "Screenshots unavailable: " + shots.Unavailable + "\n"
	}
	for _, missing := range shots.Missing {
		note += "Not pictured: " + missing + "\n"
	}
	if shots.FailedResources == nil {
		return note + "Resource failures were not recorded.\n", files, nil
	}
	listed := shots.FailedResources
	more := shots.FailedResourcesMore
	if len(listed) > verification.MaxFailedResources {
		more += len(listed) - verification.MaxFailedResources
		listed = listed[:verification.MaxFailedResources]
	}
	failures, err := json.Marshal(listed)
	if err != nil {
		return "", nil, err
	}
	total := len(listed) + more
	resource := "resources"
	if total == 1 {
		resource = "resource"
	}
	note += fmt.Sprintf("%d %s failed to load:\n%s\n", total, resource, failures)
	if more > 0 {
		note += fmt.Sprintf("and %d more\n", more)
	}
	return note, files, nil
}
