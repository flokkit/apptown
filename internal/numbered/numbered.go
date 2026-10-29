// Package numbered is the friendly reference to a change or a run,
// "<slug>#<n>": the project's current slug and the record's number within
// it. The server resolves it and the CLI checks it before asking, by this
// one grammar.
package numbered

import (
	"strconv"
	"strings"
)

// Parse reads "<slug>#<n>" with a non-empty slug and a positive n.
func Parse(s string) (slug string, n int64, ok bool) {
	slug, digits, found := strings.Cut(s, "#")
	if !found || slug == "" {
		return "", 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return slug, n, true
}
