package identity

import (
	"context"
	"errors"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/stamp"
)

var (
	ErrTokenName    = fault.New(fault.Invalid, "name required")
	ErrTokenPurpose = fault.New(fault.Invalid, "purpose required: cli (your own command line) or agent (a coding agent's connection)")
	// ErrTokenUnclassified is a token made before purposes existed. It is
	// refused until an operator records whether it is a cli or an agent token.
	ErrTokenUnclassified = errors.New("the token has no purpose; an operator must record whether it is a cli or an agent token")
)

// NewToken is a token to make. Purpose is required and has no default: an
// agent token is bounded like an OAuth connection, a cli token acts as its
// person (access.CredentialCovers). Role is the token's administrator
// ceiling, "admin" or "member"; left empty, a cli token gets its person's
// own role as it is made (it acts as them), and an agent token member (an
// agent does not inherit its person's administration).
type NewToken struct{ Name, User, Role, Purpose string }

// IssuedToken releases plaintext only after the token and receipt commit.
// Token metadata, used for subsequent listing, never contains credential material.
type IssuedToken struct {
	Token     Token
	Plaintext string
}

func (in NewToken) prepare(pr access.Principal) (NewToken, error) {
	if in.Name == "" {
		return NewToken{}, ErrTokenName
	}
	if !access.ValidTokenPurpose(in.Purpose) {
		return NewToken{}, ErrTokenPurpose
	}
	if in.User == "" {
		in.User = pr.Name
	}
	if err := ValidName(in.User); err != nil {
		return NewToken{}, err
	}
	if in.Role != "" && in.Role != "admin" {
		in.Role = "member"
	}
	return in, nil
}

func (t Token) live() bool { return !t.RevokedAt.Valid }

// revoke is the only live-to-revoked transition; the timestamp is the state.
func (t *Token) revoke(at time.Time) bool {
	if !t.live() {
		return false
	}
	t.RevokedAt = stamp.Of(at)
	return true
}

func (s *Service) IssueToken(ctx context.Context, in NewToken, pr access.Principal) (IssuedToken, error) {
	var issued IssuedToken
	err := s.administer(ctx, pr, access.CapIdentityAuth, func(tx *database.Tx, now time.Time) error {
		prepared, err := in.prepare(pr)
		if err != nil {
			return err
		}
		if prepared.Role == "" {
			// The ceiling is the person's role now: a member promoted later
			// keeps a member token, and root still needs their current grant
			// at each use (administratorFor, access.Decide).
			prepared.Role = "member"
			if prepared.Purpose == access.TokenCLI {
				root, err := RootIn(ctx, tx, prepared.User, now)
				if err != nil {
					return err
				}
				if root {
					prepared.Role = "admin"
				}
			}
		}
		issued, err = issueTokenIn(ctx, tx, prepared, pr.Name, now)
		return err
	})
	if err != nil {
		return IssuedToken{}, err
	}
	return issued, nil
}

func issueTokenIn(ctx context.Context, tx *database.Tx, in NewToken, by string, now time.Time) (IssuedToken, error) {
	plaintext := TokenPrefix + RandomHex(24)
	// The token belongs to the account's principal: a later account with
	// this name is someone else, and the token is not theirs. A token made
	// for a name with no account yet records the account, as a grant does.
	if err := recordPersonIn(ctx, tx, in.User, now); err != nil {
		return IssuedToken{}, err
	}
	name, err := queries(tx).InsertAuthToken(ctx, accesssql.InsertAuthTokenParams{Name: in.Name, Username: in.User, Role: in.Role, TokenHash: HashToken(plaintext), Purpose: in.Purpose, CreatedAt: now.UTC()})
	if err != nil {
		return IssuedToken{}, err
	}
	row, err := queries(tx).AuthTokenByName(ctx, name)
	if err != nil {
		return IssuedToken{}, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, by, ids.Nil, "token.created", in.Name, map[string]any{"user": in.User, "role": in.Role, "purpose": in.Purpose}); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token(row), Plaintext: plaintext}, nil
}

// Tokens lists live and revoked metadata under the same current-administrator
// check as mutations. No hashes or plaintext leave this operation.
func (s *Service) Tokens(ctx context.Context, pr access.Principal) ([]Token, error) {
	var out []Token
	err := s.administer(ctx, pr, access.CapIdentityAuth, func(tx *database.Tx, _ time.Time) error {
		rows, err := queries(tx).AuthTokensAll(ctx)
		if err != nil {
			return err
		}
		out = make([]Token, 0, len(rows))
		for _, r := range rows {
			out = append(out, token(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) RevokeToken(ctx context.Context, name string, pr access.Principal) (bool, error) {
	var revoked bool
	err := s.administer(ctx, pr, access.CapIdentityAuth, func(tx *database.Tx, now time.Time) error {
		var err error
		revoked, err = revokeTokenIn(ctx, tx, name, pr.Name, now)
		return err
	})
	return err == nil && revoked, err
}

func revokeTokenIn(ctx context.Context, tx *database.Tx, name, by string, now time.Time) (bool, error) {
	q := queries(tx)
	row, err := q.AuthTokenByNameForUpdate(ctx, name)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return false, err
	}
	t := token(accesssql.AuthTokensNamed(row))
	if !t.revoke(now.UTC()) {
		return false, nil
	}
	if _, err := q.RevokeAuthToken(ctx, accesssql.RevokeAuthTokenParams{Name: name, RevokedAt: t.RevokedAt.NullTime}); err != nil {
		return false, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, by, ids.Nil, "token.revoked", name, nil); err != nil {
		return false, err
	}
	return true, nil
}
