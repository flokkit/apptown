// Package ids is a record's identity: a UUIDv7 minted when the record is
// made (design/decisions/mergeable-forge-data.md). It is the only identity
// keys, references, URLs and payloads use; people see display numbers and
// names instead. The package knows nothing of AppTown.
//
// The zero ID is "no record": SQL NULL and JSON null, so an absent
// reference is never a magic value.
package ids

import (
	"bytes"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ID is a UUID, by convention a version 7 minted here.
type ID [16]byte

// Nil is the zero ID: no record.
var Nil ID

var (
	mu       sync.Mutex
	lastMs   int64
	sequence uint16 // the 12-bit counter within a millisecond
)

// New mints a UUIDv7 at now (AppTown's clock): 48 bits of Unix
// milliseconds, a 12-bit counter, then random bits. IDs minted by one
// process are strictly increasing; a clock stepped back is held at the last
// millisecond seen, so order never reverses within the process.
func New(now time.Time) ID {
	ms := now.UnixMilli()
	mu.Lock()
	switch {
	case ms > lastMs:
		lastMs, sequence = ms, 0
	default:
		// The same or an earlier millisecond: the counter carries order; when
		// it runs out, borrow the next millisecond.
		sequence++
		if sequence > 0x0fff {
			lastMs++
			sequence = 0
		}
	}
	ms, seq := lastMs, sequence
	mu.Unlock()
	var id ID
	id[0], id[1], id[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	id[3], id[4], id[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	id[6] = 0x70 | byte(seq>>8)&0x0f
	id[7] = byte(seq)
	if _, err := rand.Read(id[8:]); err != nil {
		panic(fmt.Sprintf("ids: no randomness: %v", err)) // crypto/rand does not fail on supported systems
	}
	id[8] = id[8]&0x3f | 0x80 // RFC 9562 variant
	return id
}

// IsZero reports whether id is Nil.
func (id ID) IsZero() bool { return id == Nil }

// String is the canonical form, xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx; Nil
// is "".
func (id ID) String() string {
	if id.IsZero() {
		return ""
	}
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:], id[10:])
	return string(b[:])
}

// errNotAnID: text that is not a canonical UUID.
var errNotAnID = errors.New("not a record id")

// Parse reads the canonical form (lower or upper case). "" is Nil. Anything
// else, a bare number included, is ErrNotAnID.
func Parse(s string) (ID, error) {
	if s == "" {
		return Nil, nil
	}
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Nil, fmt.Errorf("%w: %q", errNotAnID, s)
	}
	var digits [32]byte
	n := 0
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		digits[n] = s[i]
		n++
	}
	var id ID
	if _, err := hex.Decode(id[:], digits[:]); err != nil {
		return Nil, fmt.Errorf("%w: %q", errNotAnID, s)
	}
	return id, nil
}

// Value stores Nil as NULL and anything else as its text form, which
// Postgres reads into a uuid column.
func (id ID) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil
	}
	return id.String(), nil
}

// Scan reads NULL as Nil, and a uuid column's text or bytes.
func (id *ID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = Nil
		return nil
	case string:
		p, err := Parse(v)
		*id = p
		return err
	case []byte:
		if len(v) == 16 {
			copy(id[:], v)
			return nil
		}
		p, err := Parse(string(v))
		*id = p
		return err
	}
	return fmt.Errorf("ids: cannot scan %T", src)
}

// MarshalJSON is the canonical string, or null for Nil.
func (id ID) MarshalJSON() ([]byte, error) {
	if id.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(id.String())
}

// UnmarshalJSON reads a canonical string or null.
func (id *ID) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*id = Nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%w: %s", errNotAnID, b)
	}
	p, err := Parse(s)
	*id = p
	return err
}

// MarshalText and UnmarshalText make an ID a JSON map key and a flag value.
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }
func (id *ID) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	*id = p
	return err
}

// Compare orders IDs by their bytes: for v7 IDs, mint order. -1, 0 or +1.
func (id ID) Compare(other ID) int { return bytes.Compare(id[:], other[:]) }

// Format prints the canonical form for every verb, so a format string written
// for an integer id (%d, %v) prints the UUID rather than sixteen numbers.
func (id ID) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(f, "%q", id.String())
		return
	}
	fmt.Fprint(f, id.String())
}
