package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/diskspace"
)

// A worker refusal the forge acts on names its class with a code from a
// fixed set; its detail is for people. The forge reads the code, never the
// words, so rewording a refusal never changes how the forge recovers from it.
// Only a known code in a body read whole is one of these classes: a refusal
// without one, whatever its status, is only a refusal, and a body that
// cannot be read is the worker unavailable. Neither is ever "refused with
// nothing created".
//
// The forge and its worker are deployed from one build (the demo's deploy
// installs the worker, then the forge, while nothing executes), so there is
// no reader for any older refusal format.

// RefusalCode is a refusal's class on the worker protocol.
type RefusalCode string

const (
	RefusalExpired      RefusalCode = "expired"
	RefusalStartRefused RefusalCode = "start-refused"
	RefusalUnsupported  RefusalCode = "unsupported"
	RefusalNoAgentImage RefusalCode = "no-agent-image"
	RefusalMissingBlob  RefusalCode = "missing-blob"
	RefusalConflict     RefusalCode = "conflict"
	RefusalDisk         RefusalCode = "disk"
)

// RefusalContentType marks a body as a Refusal. The client classifies only
// a body that carries it.
const RefusalContentType = "application/vnd.flokkit.worker-refusal+json"

// A refusal's detail is cut to maxRefusalDetail bytes, so that encoded (at
// most six bytes a byte, escaped) it always fits the maxRefusalBody bytes
// the client reads: a refusal body longer than that was not read whole and
// is of no class.
const (
	maxRefusalDetail = 4 << 10
	maxRefusalBody   = 64 << 10
)

// Refusal is the body of a classified worker refusal. Its HTTP status is
// the refusal's status as before.
type Refusal struct {
	Code   RefusalCode `json:"code,omitempty"`
	Detail string      `json:"detail"`
}

// ErrMissingBlob: the worker lacks a blob a request names (a worker restart
// removes blobs no chat uses). The forge sends it again, once.
var ErrMissingBlob = errors.New("the worker lacks the blob")

// refusalClasses pairs each code with its error. A worker error that wraps
// several is named by the first here: a start refused because the admission
// expired is expired.
var refusalClasses = []struct {
	code  RefusalCode
	class error
}{
	{RefusalExpired, ErrExpired},
	{RefusalStartRefused, ErrStartRefused},
	{RefusalUnsupported, ErrUnsupported},
	{RefusalNoAgentImage, ErrNoAgentImage},
	{RefusalMissingBlob, ErrMissingBlob},
	{RefusalConflict, ErrTaskConflict},
	{RefusalDisk, diskspace.ErrRefused},
}

// WriteRefusal answers a refusal: err's class as its code (none when err is
// of no class), err's text as its detail, with status.
func WriteRefusal(w http.ResponseWriter, status int, err error) {
	detail := err.Error()
	if len(detail) > maxRefusalDetail {
		detail = strings.ToValidUTF8(detail[:maxRefusalDetail], "") + "…"
	}
	r := Refusal{Detail: detail}
	for _, c := range refusalClasses {
		if errors.Is(err, c.class) {
			r.Code = c.code
			break
		}
	}
	w.Header().Set("Content-Type", RefusalContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(r)
}

// workerRefusal reads a worker's answer of 300 or over. A 5xx, or a body
// that cannot be read, is the worker unavailable whatever it says; a
// classified refusal, read whole, is its class; anything else, a body
// longer than the client reads among it, is a plain refusal, whatever its
// status.
func workerRefusal(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRefusalBody+1))
	whole := len(body) <= maxRefusalBody
	body = body[:min(len(body), maxRefusalBody)]
	detail := strings.TrimSpace(string(body))
	if err != nil {
		return fmt.Errorf("%w: reading its refusal (%s): %w", ErrWorkerUnavailable, resp.Status, err)
	}
	var class error
	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); whole && media == RefusalContentType {
		var r Refusal
		if json.Unmarshal(body, &r) == nil {
			detail = r.Detail
			for _, c := range refusalClasses {
				if c.code == r.Code {
					class = c.class
				}
			}
		}
	}
	switch {
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s: %s", ErrWorkerUnavailable, resp.Status, detail)
	case class != nil:
		return classedRefusal{class, fmt.Sprintf("the worker refused: %s: %s", resp.Status, detail)}
	}
	return fmt.Errorf("the worker refused: %s: %s", resp.Status, detail)
}

// classedRefusal is a refusal of a class, in the worker's words: the detail
// already says what the class is.
type classedRefusal struct {
	class error
	text  string
}

func (c classedRefusal) Error() string { return c.text }
func (c classedRefusal) Unwrap() error { return c.class }
