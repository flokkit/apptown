package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxJSONBody = 8 << 20

type jsonBody struct {
	optional    bool
	limit       int64
	errorText   string
	errorFormat string
	// strict refuses a field the command does not know, for a command
	// whose fields change its scope: an unknown key once read as "no
	// projects named", which means the whole estate.
	strict bool
}

// jsonCommand admits only a completely decoded object to execute. Decoding
// never mutates caller-owned state; even a late type error discards the input.
// Optional bodies allow absence, not malformed input. DTO validation remains
// with the command. Unknown fields and duplicate valid fields retain Go's JSON
// semantics unless the policy is strict; null, non-objects and trailing
// values are refused.
func jsonCommand[T any](w http.ResponseWriter, r *http.Request, policy jsonBody, execute func(T)) {
	reject := func(err error) {
		if policy.errorText != "" {
			WriteError(w, 400, "%s", policy.errorText)
			return
		}
		format := policy.errorFormat
		if format == "" {
			format = "bad json: %v"
		}
		WriteError(w, 400, format, err)
	}
	limit := policy.limit
	if limit == 0 {
		limit = maxJSONBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		reject(err)
		return
	}
	body = bytes.TrimSpace(body)
	var in T
	if len(body) == 0 && policy.optional {
		execute(in)
		return
	}
	if len(body) == 0 || body[0] != '{' {
		reject(fmt.Errorf("expected an object"))
		return
	}
	if !policy.strict {
		if err := json.Unmarshal(body, &in); err != nil {
			reject(err)
			return
		}
		execute(in)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		reject(err)
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		reject(fmt.Errorf("unexpected data after the object"))
		return
	}
	execute(in)
}
