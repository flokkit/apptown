package scm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/gitexec"
)

// ObjectChange is a complete tree entry change, including identity and mode.
// Additions, deletions and type changes remain visible; names are not quoted or
// collapsed into rename summaries. It is not a human-facing diff statistic.
type ObjectChange struct {
	Path, BeforeMode, AfterMode, BeforeObject, AfterObject, Status string
}

// ReadObjectChanges compares two exact commits in an owned repository. The
// caller chooses the semantic base and serializes mutable reference selection.
func ReadObjectChanges(ctx context.Context, repo, base, tip string) ([]ObjectChange, error) {
	raw, err := gitexec.HubOutput(ctx, repo, nil, "diff-tree", "--no-commit-id", "--raw", "-r", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", "--end-of-options", base, tip, "--")
	if err != nil {
		return nil, fmt.Errorf("reading complete tree changes: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(raw), "\x00")
	if fields[len(fields)-1] != "" || len(fields)%2 != 1 {
		return nil, errors.New("incomplete Git object change inventory")
	}
	out := make([]ObjectChange, 0, len(fields)/2)
	for i := 0; i < len(fields)-1; i += 2 {
		header := strings.Fields(fields[i])
		if len(header) != 5 || !strings.HasPrefix(header[0], ":") || fields[i+1] == "" {
			return nil, errors.New("malformed Git object change inventory")
		}
		out = append(out, ObjectChange{Path: fields[i+1], BeforeMode: strings.TrimPrefix(header[0], ":"), AfterMode: header[1], BeforeObject: header[2], AfterObject: header[3], Status: header[4]})
	}
	return out, nil
}
