package identity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/stamp"
	"golang.org/x/crypto/bcrypt"
)

var ErrCredentials = errors.New("wrong username or password")
var ErrAccountDisabled = errors.New("this account is disabled")

type PasswordLogin struct {
	Username, Password string
	From               net.IP
}
type SignedIn struct {
	Principal access.Principal
	Token     string
}
type SessionIssueError struct{ Cause error }

func (e *SessionIssueError) Error() string { return fmt.Sprintf("creating your session: %v", e.Cause) }
func (e *SessionIssueError) Unwrap() error { return e.Cause }

type AdministratorDenied struct{ Reason string }

func (e *AdministratorDenied) Error() string { return e.Reason }

func (a Account) checkPassword(password string) error {
	if a.passwordHash == "" || bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password)) != nil {
		return ErrCredentials
	}
	return a.admit()
}
func (a Account) admit() error {
	if a.Disabled {
		return ErrAccountDisabled
	}
	return nil
}
func (a Account) principal(via string) access.Principal {
	return access.Principal{ID: a.PrincipalID, Kind: "user", Name: a.Username, Role: a.Role, Via: via}
}
func (a Account) tokenPrincipal(role, purpose string) access.Principal {
	pr := a.principal("token")
	pr.Purpose = purpose
	if role != "admin" || pr.Role != "admin" {
		pr.Role = "member"
	}
	return pr
}
func (d *directoryStore) login(ctx context.Context, username string) (*Account, error) {
	row, err := queries(d.db).UserByName(ctx, username)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	a, err := accountIn(ctx, d.db, row, d.now())
	return &a, err
}

// loginPrincipal is the account of a principal, nil when it has none.
func (d *directoryStore) loginPrincipal(ctx context.Context, person ids.ID) (*Account, error) {
	row, err := queries(d.db).UserByPrincipal(ctx, person)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	a, err := accountIn(ctx, d.db, row, d.now())
	return &a, err
}

// observedAccount requires the current account facts before admission. The
// existing first-observation write still records a name when it is first seen.
func (s *Service) observedAccount(ctx context.Context, username string) (Account, error) {
	now := s.now()
	person, err := personIn(ctx, s.db, username, now)
	if err != nil {
		return Account{}, err
	}
	row, err := queries(s.db).TouchUser(ctx, accesssql.TouchUserParams{Username: username, PrincipalID: person, At: now.UTC()})
	if err != nil {
		return Account{}, err
	}
	a, err := accountIn(ctx, s.db, row, now)
	if err != nil {
		return Account{}, err
	}
	if err := a.admit(); err != nil {
		return Account{}, err
	}
	return a, nil
}
func (s *Service) administratorAdmission(ctx context.Context, user string, from net.IP, via string) error {
	policy, err := s.AdministratorNetworks(ctx)
	if err == nil && policy.Allows(from) {
		return nil
	}
	payload := map[string]string{"from": from.String()}
	if via != "" {
		payload["via"] = via
	}
	if body, eventErr := strs.JSON(payload); eventErr != nil {
		log.Printf("best-effort write failed: %v", eventErr)
	} else if eventErr := audit.Append(ctx, s.db, stamp.Of(s.now()).String(), user, ids.Nil, "auth.admin-login-refused", user, body); eventErr != nil {
		log.Printf("best-effort write failed: %v", eventErr)
	}
	if err != nil {
		log.Printf("read failed: %v", err)
		return &AdministratorDenied{Reason: "the administrator network policy could not be read"}
	}
	return &AdministratorDenied{Reason: policy.Denial(from)}
}
func (s *Service) SignIn(ctx context.Context, in PasswordLogin) (SignedIn, error) {
	a, err := s.verifyLogin(ctx, in)
	if err != nil {
		return SignedIn{}, err
	}
	return s.passwordSession(ctx, a)
}

// verifyLogin reads the account and checks the password against the hash it
// read. bcrypt is slow, so this holds no lock; passwordSession then checks
// that the hash is still the account's before starting a session.
func (s *Service) verifyLogin(ctx context.Context, in PasswordLogin) (Account, error) {
	a, err := s.directory.login(ctx, in.Username)
	if err != nil {
		return Account{}, err
	}
	if a == nil {
		return Account{}, ErrCredentials
	}
	if err := a.checkPassword(in.Password); err != nil {
		return Account{}, err
	}
	if a.Role == "admin" {
		if err := s.administratorAdmission(ctx, a.Username, in.From, ""); err != nil {
			return Account{}, err
		}
	}
	return *a, nil
}

// passwordSession starts a session for a verified login only if, under the
// account's row lock, the password it was verified against is still the
// account's. A password change or reset holds the same lock while it
// replaces the password and ends the account's sessions, so a login racing
// it either commits its session first (and the change then ends it) or is
// refused here: an old password never outlives the change that replaced it.
func (s *Service) passwordSession(ctx context.Context, a Account) (SignedIn, error) {
	token, err := database.Transact(ctx, s.db, func(tx *database.Tx) (string, error) {
		row, err := queries(tx).UserByNameForUpdate(ctx, a.Username)
		if found, err := database.Lookup(err); err != nil {
			return "", err
		} else if !found || row.PasswordHash != a.passwordHash {
			return "", ErrCredentials
		} else if row.DisabledAt.Valid {
			return "", ErrAccountDisabled
		}
		token, err := issueSessionIn(ctx, tx, a.Username, s.sessions.ttl, s.now())
		if err != nil {
			return "", &SessionIssueError{Cause: err}
		}
		return token, nil
	})
	if err != nil {
		return SignedIn{}, err
	}
	if err := audit.Append(ctx, s.db, stamp.Of(s.now()).String(), a.Username, ids.Nil, "auth.login", a.Username, "null"); err != nil {
		log.Printf("best-effort write failed: %v", err)
	}
	return SignedIn{Principal: a.principal("session"), Token: token}, nil
}
func (s *Service) SessionIdentity(ctx context.Context, presented string) (access.Principal, error) {
	user, id, found, err := s.sessions.Resolve(ctx, presented)
	if !found {
		return access.Principal{}, err
	}
	// Session/token last-use writes have historically been best effort.
	if err != nil {
		log.Printf("resolving a session: %v", err)
	}
	a, err := s.observedAccount(ctx, user)
	if err != nil {
		return access.Principal{}, err
	}
	pr := a.principal("session")
	pr.Credential = SessionCredential(id)
	return pr, nil
}
func (s *Service) PersonalTokenIdentity(ctx context.Context, presented string, from net.IP) (access.Principal, error) {
	row, found, err := s.tokens.resolve(ctx, presented)
	if !found {
		return access.Principal{}, err
	}
	if err != nil {
		log.Printf("resolving a token: %v", err)
	}
	a, err := s.observedAccount(ctx, row.Username)
	if err != nil {
		return access.Principal{}, err
	}
	pr := a.tokenPrincipal(row.Role, row.Purpose)
	pr.Credential = TokenCredential(row.Name)
	user := row.Username
	if pr.Role == "admin" {
		if err := s.administratorAdmission(ctx, user, from, "token"); err != nil {
			return access.Principal{}, err
		}
	}
	return pr, nil
}
