// Package stamp represents an optional timestamp consistently in SQL and JSON.
package stamp

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"time"
)

// Time is a moment, or its absence.
//
// The columns behind it are timestamptz; a NULL there is "not yet" — a
// step not finished, a token not revoked, a grant with no expiry. On the
// wire it is what the API has always sent: the RFC3339 string, "" when
// absent, so no client changes. In Go it is a time, so comparisons are
// comparisons and arithmetic is arithmetic.
type Time struct{ sql.NullTime }

// Of wraps a time; the zero time is absence.
func Of(t time.Time) Time {
	if t.IsZero() {
		return Time{}
	}
	return Time{sql.NullTime{Time: t.UTC().Truncate(time.Second), Valid: true}}
}

// Parse reads the RFC3339 form (fractional seconds tolerated); ""
// and anything unreadable are absence.
func Parse(s string) Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return Time{}
	}
	return Time{sql.NullTime{Time: t.UTC(), Valid: true}}
}

// IsZero reports absence; json's omitzero honours it.
func (s Time) IsZero() bool { return !s.Valid }

// String is the RFC3339 form, "" when absent.
func (s Time) String() string {
	if !s.Valid {
		return ""
	}
	return s.Time.UTC().Format(time.RFC3339)
}

// Before/After compare two moments; an absent one is neither before nor
// after anything.
func (s Time) Before(o Time) bool { return s.Valid && o.Valid && s.Time.Before(o.Time) }
func (s Time) After(o Time) bool  { return s.Valid && o.Valid && s.Time.After(o.Time) }

// PassedAt reports a moment that has come and gone by now; absence has not
// passed.
func (s Time) PassedAt(now time.Time) bool { return s.Valid && s.Time.Before(now) }

func (s Time) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Time) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = Time{}
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	*s = Parse(str)
	return nil
}

// Value writes NULL for absence — never "" into a timestamptz.
func (s Time) Value() (driver.Value, error) {
	if !s.Valid {
		return nil, nil
	}
	return s.Time, nil
}
