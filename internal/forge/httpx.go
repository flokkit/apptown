package forge

import (
	"encoding/json"
	"net/http"
	"regexp"

	"flokkit.io/forge/internal/forge/httpapi"
)

// HTTP plumbing shared by every handler in the package: JSON in and out,
// path ids, the two checked-write helpers, request attribution, and the
// resolve-from-path helpers that answer the standard 404 in one place, so
// no handler repeats the read-project-by-slug preamble.

// slugRe is the one rule for names that become paths: project slugs,
// usernames, pack and service names.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Temporary pure forwards keep remaining handlers on the same HTTP encoding path.
func writeJSON(w http.ResponseWriter, code int, v any) { httpapi.WriteJSON(w, code, v) }
func writeErr(w http.ResponseWriter, code int, format string, args ...any) {
	httpapi.WriteError(w, code, format, args...)
}

// Credential protocols retain their own decoding and response vocabulary.
func readJSON(r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(dst)
}

// The two kinds of write outside a transaction. Every site was read and sorted; the name says which it is.

// actor identifies who performed a request: the authenticated identity,
// else the X-Forge-Actor header (set by the CLI and the web UI), else
// "anonymous". Attribution for the audit trail, not authentication.
func actor(r *http.Request) string {
	if id := identityFrom(r); id != nil {
		return id.User
	}
	if v := r.Header.Get("X-Forge-Actor"); v != "" {
		return v
	}
	return "anonymous"
}
