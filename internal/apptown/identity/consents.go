package identity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// A consent request is AppTown's own page asking a person to allow a
// client; it is bound to the person, the browser and the exact request
// shown, lives briefly, and is claimed once.

// RecordConsent stores a consent request under its token's hash, living
// for lasts by AppTown's clock.
func (o *oauthStore) RecordConsent(ctx context.Context, tokenHash, user, browserHash, requestJSON string, lasts time.Duration) error {
	return queries(o.db).InsertConsent(ctx, accesssql.InsertConsentParams{TokenHash: tokenHash, Username: user, BrowserHash: browserHash,
		RequestJson: json.RawMessage(requestJSON), ExpiresAt: o.now().Add(lasts).UTC()})
}

// ConsumeConsent claims a consent request once: the conditional delete is
// the claim, so two concurrent submits see one success. A request from a
// different person or browser, a modified request, or an expired one is
// not consumed.
func (o *oauthStore) ConsumeConsent(ctx context.Context, tokenHash, user, browserHash, requestJSON string) (bool, error) {
	_, err := queries(o.db).ConsumeConsent(ctx, accesssql.ConsumeConsentParams{TokenHash: tokenHash, Username: user, BrowserHash: browserHash,
		RequestJson: json.RawMessage(requestJSON), At: o.now()})
	return database.Lookup(err)
}
