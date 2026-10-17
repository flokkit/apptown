package identity

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
)

// PullTokenPrefix marks a project's read-only git token.
const PullTokenPrefix = "forge_pull_"

// ProjectOfPullToken is the project a presented token belongs to, found=false for
// an unknown token.
func (s *Service) ProjectOfPullToken(ctx context.Context, token string) (ids.ID, bool, error) {
	r, err := queries(s.db).PullTokenByHash(ctx, HashToken(token))
	found, err := database.Lookup(err)
	return r.ProjectID, found, err
}

// RuntimePullToken mints a read-only git token for the project, for one
// environment (a chat's container) to fetch main with. Only its hash is
// stored, so it cannot be handed out again: each environment gets its own,
// and every one stays valid until the project is removed. A token that
// could not be stored is an error, never handed out.
func (s *Service) RuntimePullToken(ctx context.Context, projectID ids.ID) (string, error) {
	tok := PullTokenPrefix + RandomHex(24)
	if err := queries(s.db).InsertPullToken(ctx, accesssql.InsertPullTokenParams{TokenHash: HashToken(tok), ProjectID: projectID, CreatedAt: s.now().UTC()}); err != nil {
		return "", err
	}
	return tok, nil
}
