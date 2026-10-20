package forge

import (
	"errors"

	"flokkit.io/forge/internal/forge/secret"
)

// Test-side readers over rows and errors. apptownd reads through the record
// stores; the tests still look at raw payloads.

// isSecretsMissing: the resolve failed only because bindings are absent.
func isSecretsMissing(err error) bool {
	var m *secret.MissingError
	return errors.As(err, &m)
}
