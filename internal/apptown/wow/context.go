package wow

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
)

// RepositoryContext declares a bounded input from the session's selected commit.
// It is stored with a guidance body's immutable version, like its skill.
type RepositoryContext struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"maxBytes"`
}

// SplitContext reads a single context declaration before the guidance prose:
// <!-- context: [{"path":"PROJECT-NOTES.md","maxBytes":6144}] -->
// Keeping the declaration in the body gives authored and shipped guidance the
// same versioning, editing, hashing and adoption contract.
func SplitContext(body string) (string, []RepositoryContext, error) {
	const prefix = "<!-- context:"
	var out []RepositoryContext
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
			continue
		}
		if out != nil || !strings.HasSuffix(line, "-->") {
			return "", nil, fault.New(fault.Invalid, "guidance needs one context declaration ending in -->")
		}
		raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, prefix), "-->"))
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&out); err != nil || dec.More() || len(out) == 0 || len(out) > 16 {
			return "", nil, fault.New(fault.Invalid, "guidance context must declare 1–16 repository files")
		}
		seen := map[string]bool{}
		for _, f := range out {
			if f.Path == "" || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "/") || f.Path == ".." || strings.HasPrefix(f.Path, "../") || strings.ContainsAny(f.Path, "\\\x00\r\n:") || f.Path == ".git" || strings.HasPrefix(f.Path, ".git/") || seen[f.Path] || f.MaxBytes < 1 || f.MaxBytes > 65536 {
				return "", nil, fmt.Errorf("%w: %q needs a unique repository-relative path and maxBytes from 1 to 65536", fault.New(fault.Invalid, "invalid guidance context"), f.Path)
			}
			seen[f.Path] = true
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), out, nil
}
