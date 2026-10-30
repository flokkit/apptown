package deploy

import (
	"context"
	"errors"
	"net"
	"net/url"
)

// terminalError marks confirmed rejection by a deployment provider.
type terminalError struct{ error }

func Terminal(err error) error { return terminalError{err} }

func IsTerminal(err error) bool {
	var t terminalError
	return errors.As(err, &t)
}

// Refused reports whether a provider definitely refused a request: it
// answered and said no (a terminal rejection, or an answer that says the
// request itself was refused), so the effect did not happen. Anything else
// (a timeout, a lost connection, a server error) leaves whether the effect
// happened unknown, and a caller must say so rather than report a failure.
func Refused(err error) bool {
	if err == nil {
		return false
	}
	if IsTerminal(err) {
		return true
	}
	var r interface{ Refused() bool }
	return errors.As(err, &r) && r.Refused()
}

// Unreachable marks a provider connection failure. Rejections and invalid
// configuration are not connectivity failures. Wrapping preserves the cause.
func Unreachable(err error) error {
	if err == nil {
		return nil
	}
	return unreachableError{err}
}

type unreachableError struct{ error }

func (e unreachableError) Unwrap() error { return e.error }
func IsUnreachable(err error) bool       { var e unreachableError; return errors.As(err, &e) }

// ConnectionError classifies typed transport failures at a provider adapter.
// A cancellation, HTTP rejection or invalid response does not prove unreachability.
func ConnectionError(err error) error {
	if err == nil || (errors.Is(err, context.Canceled)) {
		return err
	}
	cause := err
	var request *url.Error
	if errors.As(err, &request) {
		cause = request.Err
	}
	var network net.Error
	if errors.As(cause, &network) && network != context.DeadlineExceeded {
		return Unreachable(err)
	}
	return err
}
