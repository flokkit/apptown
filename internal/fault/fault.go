// Package fault classifies errors by what the caller can do about them, so
// every transport answers the same failure the same way. An owner marks its
// own errors with a Kind; HTTP maps a Kind to a status and MCP to a readable
// refusal, each in one place, instead of every handler re-deriving the table.
//
// The Kind describes the failure, not the transport: NotFound, not 404.
package fault

import (
	"errors"
	"fmt"
)

// Kind is what went wrong, from the caller's side.
type Kind uint8

const (
	// Internal is an unclassified failure: the caller cannot fix it.
	Internal Kind = iota
	// Invalid input: the caller can correct the request.
	Invalid
	// Unauthenticated: no verified caller.
	Unauthenticated
	// Forbidden: the caller is known and may not do this.
	Forbidden
	// NotFound: the thing addressed does not exist (or is not visible).
	NotFound
	// Conflict: the request clashes with the current state; retrying after
	// reading it again may succeed.
	Conflict
	// Unprocessable: well-formed, but the current state cannot accept it.
	Unprocessable
	// Unavailable: a dependency is down or busy; retry later.
	Unavailable
)

var names = [...]string{"internal", "invalid", "unauthenticated", "forbidden", "not found", "conflict", "unprocessable", "unavailable"}

func (k Kind) String() string {
	if int(k) < len(names) {
		return names[k]
	}
	return fmt.Sprintf("kind(%d)", k)
}

// Classified is implemented by an error that knows its Kind. Owners with their
// own error types (a denial with a code, a conflict with detail) implement it
// rather than wrapping.
type Classified interface {
	error
	FaultKind() Kind
}

// Error is an error with a Kind. It wraps an underlying error when there is one.
type Error struct {
	kind Kind
	msg  string
	err  error
}

func (e *Error) Error() string {
	switch {
	case e.msg != "" && e.err != nil:
		return e.msg + ": " + e.err.Error()
	case e.msg != "":
		return e.msg
	case e.err != nil:
		return e.err.Error()
	}
	return e.kind.String()
}

func (e *Error) Unwrap() error   { return e.err }
func (e *Error) FaultKind() Kind { return e.kind }

// New returns a classified error with a message, for an owner's sentinel:
//
//	var ErrConflict = fault.New(fault.Conflict, "the record changed")
//
// errors.Is still matches the sentinel by identity.
func New(kind Kind, msg string) error { return &Error{kind: kind, msg: msg} }

// Wrap classifies err, keeping it in the chain. A nil err stays nil.
func Wrap(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	return &Error{kind: kind, err: err}
}

// Message is what the caller should read. A sentinel made with New only names
// a category, and the text wrapped around it carries the specifics, so the
// whole chain reads. An owner's own error type (a denial, a refused retry) is
// complete in itself, so only its words read, without the context layers
// added on the way out.
func Message(err error) string {
	var c Classified
	if errors.As(err, &c) {
		if _, sentinel := c.(*Error); !sentinel {
			return c.Error()
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// KindOf reports the Kind of the first classified error in err's chain, or
// Internal when nothing in the chain says.
func KindOf(err error) Kind {
	var c Classified
	if errors.As(err, &c) {
		return c.FaultKind()
	}
	return Internal
}

// Coded is implemented by an error that carries a stable machine code, such as
// a denial's reason code, for clients that branch on it.
type Coded interface {
	error
	FaultCode() string
}

// CodeOf returns the code of the first coded error in err's chain, or "".
func CodeOf(err error) string {
	var c Coded
	if errors.As(err, &c) {
		return c.FaultCode()
	}
	return ""
}
