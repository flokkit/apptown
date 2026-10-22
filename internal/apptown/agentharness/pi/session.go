package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Session files. Pi names a session's transcript <time>_<session id>.jsonl
// and finds it again by the id in its first line, the session header;
// --session-id alone does not fix the file's name. So whoever wants to know
// the file before Pi starts makes it, with its header, and Pi appends to it
// (as console/pi/provision.go does for chats). The header is Pi's own
// minimal record, version 3.
const (
	sessionHeaderTime = "2006-01-02T15:04:05.000Z"
	sessionFileTime   = "2006-01-02T15-04-05"
)

// SessionFileName names the transcript of session id begun at the time
// given, as Pi names its own.
func SessionFileName(id string, at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("%s-%03dZ_%s.jsonl", at.Format(sessionFileTime), at.Nanosecond()/int(time.Millisecond), id)
}

// SessionHeader is the first line of a new session's transcript, with its
// newline: the session's id, when it began and the folder Pi works in.
func SessionHeader(id string, at time.Time, cwd string) []byte {
	return fmt.Appendf(nil, "{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":%q,\"cwd\":%q}\n", id, at.UTC().Format(sessionHeaderTime), cwd)
}

// SessionHeaderID is the session id a transcript's header names; an error
// when its first line is not a session header.
func SessionHeaderID(transcript []byte) (string, error) {
	first, _, _ := bytes.Cut(transcript, []byte("\n"))
	var header struct{ Type, ID string }
	if err := json.Unmarshal(first, &header); err != nil || header.Type != "session" || header.ID == "" {
		return "", errors.New("the transcript does not begin with a session header")
	}
	return header.ID, nil
}
