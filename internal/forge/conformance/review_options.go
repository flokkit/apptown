package conformance

import "flokkit.io/forge/internal/ids"

// ReviewOptions shape one review: which projects (nil = the whole estate) and
// whether a cache miss on an exec-image tool may BUILD the app image (bounded
// by a per-review budget; design/subsystems/conformance-surface.md §6).
type ReviewOptions struct {
	Projects    []ids.ID
	Build       bool
	BuildBudget int // 0 → default
}
