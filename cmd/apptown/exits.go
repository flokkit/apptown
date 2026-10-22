package main

import (
	"errors"
	"fmt"
)

// The exit codes every command shares, beside each command's own outcome
// codes (0–4): wait's 2 means a person is needed and 1 that the work
// failed, so neither may also mean "you typed it wrong" or "AppTown is
// out of reach". 64 and 69 are sysexits.h's EX_USAGE and EX_UNAVAILABLE.
const (
	// exitUsage: the command line was wrong (an unknown command or flag, a
	// missing argument, an argument of the wrong shape); no change was sent
	// (a reference may have been looked up first).
	exitUsage = 64
	// exitUnreachable: AppTown could not be reached, or kept answering that
	// it was unavailable after the CLI's retries.
	exitUnreachable = 69
)

// usageError is a command line the CLI refuses before sending anything.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef is a usageError, formatted as fmt.Sprintf.
func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// unreachable is a request that got no answer from AppTown: the connection
// failed, or a gateway kept answering 502, 503 or 504. A mutation that may
// have left is an unknownOutcome instead.
type unreachable struct{ err error }

func (e *unreachable) Error() string { return e.err.Error() }
func (e *unreachable) Unwrap() error { return e.err }

// exitCode is the code the CLI ends with for a command's error.
func exitCode(err error) int {
	var unknown *unknownOutcome
	var opUnknown *operationUnknown
	var usage *usageError
	var gone *unreachable
	switch {
	case errors.As(err, &unknown), errors.As(err, &opUnknown):
		return exitUnknown
	case errors.As(err, &usage):
		return exitUsage
	case errors.As(err, &gone):
		return exitUnreachable
	}
	return 1
}
