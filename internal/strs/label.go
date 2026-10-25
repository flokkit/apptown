package strs

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DisplayLabel normalizes customer-facing text without treating it as an
// identifier. Stable machine names and resource identities remain separate.
func DisplayLabel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 128 {
		return "", errors.New("display label must contain 1 to 128 characters")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("display label cannot contain control characters")
		}
	}
	return value, nil
}
