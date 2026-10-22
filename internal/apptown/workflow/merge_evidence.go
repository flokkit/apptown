package workflow

import "slices"

type mergedEvidence struct {
	Merged bool `json:"merged"`
}

type MergeEvidence struct {
	Conflicts      []string `json:"conflicts,omitempty"`
	OutOfBandFiles []string `json:"outOfBandFiles,omitempty"`
	Reason         string   `json:"reason"`
}

// A conflict is a Git problem; drift is an integrity event with its own remedy.
func MergeBlockEvidence(conflicts, drift []string) MergeEvidence {
	if len(drift) > 0 {
		return MergeEvidence{OutOfBandFiles: slices.Clone(drift), Reason: "the project's stored files have edits that did not come through AppTown — " +
			"there is no authorized way to do that. An admin must decide on the project page: " +
			"keep those edits or throw them away. Then re-check this step."}
	}
	return MergeEvidence{Conflicts: slices.Clone(conflicts), Reason: "merge conflict: resolve in the project workspace (git add, git commit), then re-check"}
}
