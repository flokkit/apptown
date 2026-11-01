// Package enum holds the storage and JSON codec shared by small state enums:
// a uint8 whose values run 1..n, each spelled by one word. A type keeps its own
// constants, names table and transition rules, and forwards its String, Parse,
// Value, Scan and JSON methods here in one line each.
package enum

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Set is the codec of one enum type. The zero value of T is never valid.
type Set[T ~uint8] struct {
	// Names spells each value: Names[i] is the word for T(i+1).
	Names []string
	// Invalid is what String returns for a value outside the set.
	Invalid string
	// Err is wrapped by every parse, range and scan error, so callers can
	// test for it with errors.Is.
	Err error
	// ScanLabel names a non-text database value in a Scan error, such as
	// "value" or "database value".
	ScanLabel string
}

// valid reports whether v is one of the set's values.
func (s Set[T]) valid(v T) bool { return v >= 1 && int(v) <= len(s.Names) }

// String is v's word, or Invalid.
func (s Set[T]) String(v T) string {
	if !s.valid(v) {
		return s.Invalid
	}
	return s.Names[v-1]
}

// Parse reads a word back into its value.
func (s Set[T]) Parse(text string) (T, error) {
	for i, name := range s.Names {
		if name == text {
			return T(i + 1), nil
		}
	}
	return 0, fmt.Errorf("%w: %q", s.Err, text)
}

// Value stores v as its word; a value outside the set is refused.
func (s Set[T]) Value(v T) (driver.Value, error) {
	if !s.valid(v) {
		return nil, fmt.Errorf("%w: %d", s.Err, v)
	}
	return s.Names[v-1], nil
}

// Scan reads a stored word into dst. Only text is accepted: NULL and other
// types are errors, and dst is left unchanged on any error.
func (s Set[T]) Scan(dst *T, value any) error {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("%w: %s %T", s.Err, s.ScanLabel, value)
	}
	parsed, err := s.Parse(text)
	if err != nil {
		return err
	}
	*dst = parsed
	return nil
}

// JSON writes v as its word; a value outside the set is an error.
func (s Set[T]) JSON(v T) ([]byte, error) {
	value, err := s.Value(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// ReadJSON reads a JSON string word into dst.
func (s Set[T]) ReadJSON(dst *T, data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	return s.Scan(dst, text)
}
