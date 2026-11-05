package projectnotes

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/gitexec"
)

// Read reads only the named commit in the repository. Symlinks and invalid
// content are errors; an absent file is an empty list with present=false.
func Read(ctx context.Context, repo, commit string) (notes []Note, present bool, err error) {
	entry, err := gitexec.Run(ctx, repo, "ls-tree", "-l", commit, "--", Path)
	if err != nil {
		return nil, false, err
	}
	if entry == "" {
		return []Note{}, false, nil
	}
	fields := strings.Fields(entry)
	if len(fields) < 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, true, fmt.Errorf("%s must be a regular file", Path)
	}
	size, err := strconv.Atoi(fields[3])
	if err != nil {
		return nil, true, err
	}
	if size > maxBytes {
		return nil, true, fmt.Errorf("%s:1: exceeds %d bytes", Path, maxBytes)
	}
	body, err := gitexec.OutputBytes(ctx, repo, nil, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, true, err
	}
	notes, err = parse(body)
	return notes, true, err
}
