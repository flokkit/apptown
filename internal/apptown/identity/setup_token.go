package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// The first administrator of an internal-auth AppTown is whoever gives the
// setup token: a random value AppTown writes to a file on its own host
// while no account exists. Reading that file is the proof of being the
// operator, as running -reset-password on the host is. Without it the
// first visitor to the login page would become the administrator.

// ErrSetupToken refuses a first account made without the setup token.
var ErrSetupToken = errors.New("the setup token is missing or wrong")

// setupTokenError names where the token is, so the person can fetch it.
type setupTokenError struct{ Path string }

func (e *setupTokenError) Error() string {
	return fmt.Sprintf("%v: it is in %s on AppTown's host", ErrSetupToken, e.Path)
}

func (e *setupTokenError) Unwrap() error { return ErrSetupToken }

// EnsureSetupToken makes the setup token while setup is not complete (the
// durable setup marker NeedsBootstrap reads; accounts recorded in open mode
// do not complete it) and returns it, or "" once setup is complete or when
// AppTown keeps none (no path configured). An existing token is kept, so a
// restart does not invalidate one already handed out; once setup is
// complete a token left behind is removed.
func (s *Service) EnsureSetupToken(ctx context.Context) (string, error) {
	path := s.cfg.SetupTokenPath
	if path == "" {
		return "", nil
	}
	needs, err := s.NeedsBootstrap(ctx)
	if err != nil {
		return "", err
	}
	if !needs {
		return "", s.spendSetupToken()
	}
	if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw)), nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// SetupTokenPath is where the setup token is written; "" when AppTown
// keeps none.
func (s *Service) SetupTokenPath() string { return s.cfg.SetupTokenPath }

// checkSetupToken is Bootstrap's first question: did the caller give the
// token on this host? An AppTown configured with no path checks nothing.
func (s *Service) checkSetupToken(ctx context.Context, given string) error {
	if s.cfg.SetupTokenPath == "" {
		return nil
	}
	want, err := s.EnsureSetupToken(ctx)
	if err != nil {
		return err
	}
	if want == "" || given == "" || subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(given))) != 1 {
		return &setupTokenError{Path: s.cfg.SetupTokenPath}
	}
	return nil
}

// spendSetupToken removes the token once the first account is committed;
// a failure to remove it is reported, and bootstrap is refused anyway now
// that an account exists.
func (s *Service) spendSetupToken() error {
	if s.cfg.SetupTokenPath == "" {
		return nil
	}
	if err := os.Remove(s.cfg.SetupTokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
