package deploy

import "errors"

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
