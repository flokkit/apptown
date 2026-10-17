package scm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/execution"
)

// Drift lists uncommitted paths in a working tree (modified, deleted,
// untracked). On a forge-managed project repo every sanctioned writer
// commits and leaves the tree clean — so ANY drift is an edit that
// bypassed the forge: an integrity event, never routine state.
func Drift(ctx context.Context, runner execution.Runner, repo string) ([]string, error) {
	// NUL separation preserves whitespace, quotes, newlines and rename paths.
	out := runner.Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: []string{"git", "status", "--porcelain=v1", "-z", "--", ".", ":(exclude).forge", ".forge/app.yaml", ".forge/compose.yaml", ".forge/app.json"}, Timeout: 10 * time.Minute})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.RunErr != nil {
		return nil, fmt.Errorf("inspecting repository edits: %w", out.RunErr)
	}
	var files []string
	paths := strings.Split(string(out.Output), "\x00")
	for i := 0; i < len(paths); i++ {
		entry := paths[i]
		if entry == "" {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("invalid repository status record")
		}
		files = append(files, entry[3:])
		if strings.ContainsAny(entry[:2], "RC") {
			i++
			if i >= len(paths) || paths[i] == "" {
				return nil, fmt.Errorf("missing repository rename source")
			}
			files = append(files, paths[i])
		}
	}
	return files, nil
}
