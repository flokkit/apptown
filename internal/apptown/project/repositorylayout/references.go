package repositorylayout

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// Reference maps one identified local directory. Its descendants retain their
// relative names. Backend keys, display labels and arbitrary text are not paths.
type Reference struct {
	Owner  ids.ID `json:"owner"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Translate resolves only an exact owned root or separator-bounded descendant.
// A match belonging to another project refuses instead of redirecting its data.
func Translate(references []Reference, owner ids.ID, path string) (string, bool, error) {
	if owner.IsZero() {
		return "", false, fmt.Errorf("local reference needs project identity")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false, fmt.Errorf("local reference is not an absolute clean path: %q", path)
	}
	selected := -1
	for i, r := range references {
		if r.Owner.IsZero() || !filepath.IsAbs(r.Before) || !filepath.IsAbs(r.After) || filepath.Clean(r.Before) != r.Before || filepath.Clean(r.After) != r.After {
			return "", false, fmt.Errorf("invalid local relocation mapping")
		}
		if path != r.Before && !strings.HasPrefix(path, r.Before+string(filepath.Separator)) {
			continue
		}
		if r.Owner != owner {
			return "", false, fmt.Errorf("local reference selects another project's directory: %s", path)
		}
		if selected >= 0 {
			return "", false, fmt.Errorf("overlapping local relocation mappings for %s", path)
		}
		selected = i
	}
	if selected < 0 {
		return path, false, nil
	}
	r := references[selected]
	rel, err := filepath.Rel(r.Before, path)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(r.After, rel), true, nil
}
