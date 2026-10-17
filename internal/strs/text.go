// Package strs holds the small string helpers that were copied into many
// packages: cutting a message to a length for logs, errors and UI, and
// picking the first value that says something, naming a commit, and
// encoding a value as JSON without a silent stand-in.
package strs

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Truncate returns s unchanged when it is at most n bytes, and otherwise the
// longest prefix of at most n bytes that ends on a character boundary, followed
// by "…". It never splits a multi-byte UTF-8 character, so the result stays
// valid text in JSON, logs and the web UI.
func Truncate(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Tail returns s unchanged when it is at most n bytes, and otherwise its last
// n bytes or fewer, starting on a character boundary, after a line that says
// how many characters came before. A command's failure is usually at the end
// of its output, after its progress and setup lines, so diagnostics keep the
// end rather than the start.
func Tail(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return fmt.Sprintf("…(%d earlier characters omitted)\n", utf8.RuneCountInString(s[:cut])) + s[cut:]
}

// FirstNonBlank returns the first value that is not empty or whitespace, or ""
// when there is none. Unlike cmp.Or, a whitespace-only value counts as blank.
func FirstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ShortSHA is how a commit is named to a person: its first 12 hex digits,
// enough to stay unique in any repository a forge holds, or "(none)" when
// there is no commit. Every message and view uses this one form so the same
// commit reads the same everywhere.
func ShortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "(none)"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// JSON is v encoded as JSON text. A value that cannot be encoded is an
// error for the caller to report: a record written with a stand-in such as
// "{}" or "null" would say something the value did not.
func JSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encoding %T as JSON: %w", v, err)
	}
	return string(b), nil
}
