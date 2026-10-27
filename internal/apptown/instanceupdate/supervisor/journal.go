package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/ids"
)

// Intention preserves a request, not an authorization to stop the application.
// Principal and scope are supplied only by the authenticated daemon peer.
type Intention struct {
	Request             ids.ID `json:"request"`
	Instance            ids.ID `json:"instance"`
	ConfigurationDigest string `json:"configurationDigest"`
	CandidateSHA256     string `json:"candidateSHA256"`
	Principal           ids.ID `json:"principal"`
	CredentialScope     string `json:"credentialScope"`
	PolicyRevision      string `json:"policyRevision,omitempty"`
}

type Transaction struct {
	Intention   Intention `json:"intention"`
	RequestedAt time.Time `json:"requestedAt"`
	Phase       string    `json:"phase"`
}

type journal struct {
	Format              int           `json:"format"`
	ConfigurationDigest string        `json:"configurationDigest"`
	Transactions        []Transaction `json:"transactions"`
}

// Store holds external installation exclusion until Close. A new process must
// read and validate the same durable journal before any registered start.
type Store struct {
	mu           sync.Mutex
	registration Registration
	lock         *os.File
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing control data")
	}
	return nil
}

func readRecord(path string, dst any) error {
	if err := secureFile(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 8<<20 {
		return errors.New("external control record exceeds limit")
	}
	return decode(raw, dst)
}

func writeRecord(dir, name string, value any, noReplace bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	exists, err := durable.Write(dir, name, append(raw, '\n'), 0600, noReplace)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("external control record already exists")
	}
	return nil
}

// Open acquires the installation's external lock; missing or corrupt journal is
// an error, not an idle installation. No daemon/database access is required.
func Open(dir string) (*Store, error) {
	lock, err := lockState(dir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Store, error) { _ = lock.Close(); return nil, err }
	var r Registration
	if err := readRecord(filepath.Join(dir, "registration.json"), &r); err != nil {
		return fail(err)
	}
	if err := r.validate(); err != nil {
		return fail(err)
	}
	if r.StateDir != dir {
		return fail(errors.New("registered state directory differs"))
	}
	s := &Store{registration: r, lock: lock}
	if _, err := s.read(); err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}
func (s *Store) Registration() Registration { return s.registration }

func (s *Store) read() (journal, error) {
	var j journal
	if s.lock == nil {
		return j, errors.New("supervisor ownership is closed")
	}
	if err := readRecord(filepath.Join(s.registration.StateDir, "journal.json"), &j); err != nil {
		return j, err
	}
	if j.Format != Protocol || j.ConfigurationDigest != s.registration.ConfigurationDigest {
		return j, errors.New("journal registration differs")
	}
	seen := map[ids.ID]bool{}
	active := false
	for _, t := range j.Transactions {
		if err := s.validateIntention(t.Intention); err != nil {
			return j, err
		}
		if seen[t.Intention.Request] || t.RequestedAt.IsZero() {
			return j, errors.New("invalid journal request identity")
		}
		seen[t.Intention.Request] = true
		switch t.Phase {
		case "cancelled":
		case "staged", "draining", "stopped", "snapshot-verified", "candidate-installed", "verifying", "opening", "running", "recovering", "recovered":
			if active {
				return j, errors.New("journal has overlapping transactions")
			}
			active = true
		default:
			return j, errors.New("unknown external journal phase")
		}
	}
	return j, nil
}
func (s *Store) validateIntention(r Intention) error {
	if r.Request == ids.Nil || r.Instance != s.registration.Instance || r.ConfigurationDigest != s.registration.ConfigurationDigest || !hexDigest.MatchString(r.CandidateSHA256) || r.Principal == ids.Nil || r.CredentialScope == "" || len(r.CredentialScope) > 4096 || len(r.PolicyRevision) > 256 {
		return errors.New("invalid staged update identity")
	}
	return nil
}

// Stage records the already authenticated daemon's intention. It grants no
// stop, install, snapshot or recovery permission and changes no process.
func (s *Store) Stage(in Intention, now time.Time) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateIntention(in); err != nil {
		return Transaction{}, err
	}
	j, err := s.read()
	if err != nil {
		return Transaction{}, err
	}
	for _, t := range j.Transactions {
		if t.Intention.Request == in.Request {
			if t.Intention != in {
				return Transaction{}, errors.New("request UUID was used for different update input")
			}
			return t, nil
		}
	}
	for _, t := range j.Transactions {
		if t.Phase != "cancelled" {
			return Transaction{}, errors.New("another update intention remains unsettled")
		}
	}
	if now.IsZero() {
		return Transaction{}, errors.New("request time is required")
	}
	t := Transaction{Intention: in, RequestedAt: now.UTC(), Phase: "staged"}
	j.Transactions = append(j.Transactions, t)
	if err := writeRecord(s.registration.StateDir, "journal.json", j, false); err != nil {
		return Transaction{}, err
	}
	return t, nil
}

func (s *Store) Cancel(request ids.ID) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read()
	if err != nil {
		return Transaction{}, err
	}
	for i, t := range j.Transactions {
		if t.Intention.Request != request {
			continue
		}
		if t.Phase == "cancelled" {
			return t, nil
		}
		if t.Phase != "staged" {
			return Transaction{}, errors.New("only an unstopped intention can be cancelled")
		}
		j.Transactions[i].Phase = "cancelled"
		if err := writeRecord(s.registration.StateDir, "journal.json", j, false); err != nil {
			return Transaction{}, err
		}
		return j.Transactions[i], nil
	}
	return Transaction{}, errors.New("update request is unknown")
}

// OrdinaryStart refuses every maintenance or ambiguous opening phase. U2 can
// start only the registered original installation; U3/U4 add evidenced modes.
func (s *Store) OrdinaryStart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startupFailure(); err != nil {
		return err
	}
	j, err := s.read()
	if err != nil {
		return err
	}
	for _, t := range j.Transactions {
		if t.Phase != "staged" && t.Phase != "cancelled" {
			return fmt.Errorf("ordinary startup is fenced by update %s (%s)", t.Intention.Request, t.Phase)
		}
	}
	return nil
}

func (s *Store) Transactions() ([]Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read()
	return j.Transactions, err
}
