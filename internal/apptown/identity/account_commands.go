package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/database"
	"golang.org/x/crypto/bcrypt"
)

// NewAccount is the first administrator's chosen credentials.
type NewAccount struct {
	Username, Password string
	// SetupToken is the first account's proof of the host (setup_token.go).
	SetupToken string `json:"setupToken"`
}

// AccountChange creates or updates an account. An omitted password keeps the
// existing one; an omitted role leaves the account's authority unchanged.
type AccountChange struct {
	Username, Password, Role string
	Disabled                 *bool
	DefaultGroup             *string
}

// AccountInputError describes an invalid account command, without credentials.
type AccountInputError struct{ Reason string }

func (e *AccountInputError) Error() string         { return e.Reason }
func (e *AccountInputError) FaultKind() fault.Kind { return fault.Invalid }

var directoryName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (in NewAccount) prepare() (string, error) {
	if !directoryName.MatchString(in.Username) || len(in.Password) < 8 {
		return "", &AccountInputError{Reason: "username (slug) and a password of at least 8 characters"}
	}
	return hashPassword(in.Password)
}

// prepare checks the request; the name is checked against the account it
// names, in changeAccountIn, because an existing account is changed whatever
// its name's syntax (a single sign-on name such as an email address).
func (in AccountChange) prepare() (accountEdit, error) {
	if in.Username == "" {
		return accountEdit{}, &AccountInputError{Reason: "username (slug) required"}
	}
	// An empty role leaves the account's authority as it is (a disable, an
	// enable, a password reset); only "admin" or "member" changes it.
	if in.Role != "" && in.Role != "admin" && in.Role != "member" {
		return accountEdit{}, &AccountInputError{Reason: "role must be admin or member"}
	}
	if in.Password != "" && len(in.Password) < 8 {
		return accountEdit{}, &AccountInputError{Reason: "passwords are at least 8 characters"}
	}
	edit := accountEdit{Name: in.Username, Role: in.Role, Disabled: in.Disabled}
	if in.DefaultGroup != nil {
		clean := strings.TrimSpace(*in.DefaultGroup)
		edit.DefaultGroup = &clean
	}
	if in.Password != "" {
		hash, err := hashPassword(in.Password)
		if err != nil {
			return accountEdit{}, err
		}
		edit.PasswordHash = hash
	}
	return edit, nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// Bootstrap commits the first account, defaults, receipt and session together.
// The transport writes only a committed cookie; hashing holds no transaction.
func (s *Service) Bootstrap(ctx context.Context, in NewAccount) (SignedIn, error) {
	if err := ctx.Err(); err != nil {
		return SignedIn{}, err
	}
	needs, err := s.NeedsBootstrap(ctx)
	if err != nil {
		return SignedIn{}, err
	}
	if !needs {
		return SignedIn{}, ErrAlreadyBootstrapped
	}
	if err := s.checkSetupToken(ctx, in.SetupToken); err != nil {
		return SignedIn{}, err
	}
	hash, err := in.prepare()
	if err != nil {
		return SignedIn{}, err
	}
	now := s.now()
	signed, err := database.Transact(ctx, s.db, func(tx *database.Tx) (SignedIn, error) {
		a, err := bootstrapIn(ctx, tx, in.Username, hash, now)
		if err != nil {
			return SignedIn{}, err
		}
		token, err := issueSessionIn(ctx, tx, a.Username, s.cfg.SessionTTL, now)
		if err != nil {
			return SignedIn{}, &SessionIssueError{Cause: err}
		}
		return SignedIn{Principal: a.principal("session"), Token: token}, nil
	})
	if err != nil {
		return SignedIn{}, err
	}
	// The account is committed: a token left behind is harmless (bootstrap
	// is refused now), so failing to remove it does not undo the sign-in.
	if err := s.spendSetupToken(); err != nil {
		log.Printf("removing the setup token %s: %v", s.cfg.SetupTokenPath, err)
	}
	return signed, nil
}

// SaveUser checks the verified caller against the current account before
// preparing and atomically applying the account change.
func (s *Service) SaveUser(ctx context.Context, in AccountChange, pr access.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reject an unauthorized password change before paying bcrypt's cost;
	// authority is checked again with the mutation under the users lock.
	if in.Password != "" {
		if err := s.requireAdministration(ctx, s.db, pr, access.CapIdentityUsers, s.now()); err != nil {
			return err
		}
	}
	edit, err := in.prepare()
	if err != nil {
		return err
	}
	return s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		return changeAccountIn(ctx, tx, edit, pr.Name, now)
	})
}

// OperatorActor is who the ledger names for a host operator's break-glass
// password reset: someone with AppTown's own settings on its host, not a
// signed-in account.
const OperatorActor = "apptown-operator (host)"

// ResetPassword is the operator's break-glass for a locked-out account: it
// gives an existing account a new random password through the same change an
// administrator's password edit makes (its sessions end, one receipt), leaves
// its role and disabled state alone, and returns the password. An unknown
// name is ErrNoSuchName; no account is created.
func (s *Service) ResetPassword(ctx context.Context, username string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		_, err := queries(tx).UserByNameForUpdate(ctx, username)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNoSuchName, username)
		} else if err != nil {
			return err
		}
		return changeAccountIn(ctx, tx, accountEdit{Name: username, PasswordHash: hash}, OperatorActor, s.now())
	})
	if err != nil {
		return "", err
	}
	return password, nil
}
