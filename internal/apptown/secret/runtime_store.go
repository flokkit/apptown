package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
)

// StoreInfo describes a store for the Settings page.
type StoreInfo struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

func (a *Service) Stores() []StoreInfo {
	out := []StoreInfo{{
		Name: StoreInternal, Title: "Built-in store", Available: true,
		Note: "Part of AppTown itself: no separate service, nothing to run. Values are kept in AppTown's own database, obfuscated with a key AppTown generated. That stops accidental disclosure; it is not a vault, and anyone holding the database can read the values. For anything that matters, connect a real secret store.",
	}}
	inf := StoreInfo{Name: StoreInfisical, Title: "Infisical", Available: infisicalConfig(a.fileSecrets()).Configured(),
		Note: "A binding names a key in the Infisical project and environment this AppTown is configured for. Values never enter AppTown's database."}
	if !inf.Available {
		inf.Note = "N" + ErrInfisicalNotSetUp.Error()[1:] + "."
	}
	return append(out, inf)
}

// --- the built-in store ------------------------------------------------------
// Application delivery and settings use the built-in Vault through these helpers.

// Stored returns the latest version of a value. Any lookup failure returns
// ok=false; failures other than ErrNotStored are logged. Use Vault.Get when the
// caller needs the error rather than this convenience fallback.
func (a *Service) Stored(ctx context.Context, scope, name string) (value string, version int, ok bool) {
	value, version, err := a.Vault.Get(ctx, scope, name)
	if err != nil {
		if !errors.Is(err, ErrNotStored) {
			log.Printf("reading the stored secret %s: %v", name, err)
		}
		return "", 0, false
	}
	return value, version, true
}

// GenerateValue mints a value for a generated secret: 32 random bytes as
// hex, which is what every framework's "secret key base" wants.
func GenerateValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- Infisical --------------------------------------------------------------

// InfisicalValues is the Infisical map alone — no secrets.env overrides —
// so an application binding resolves against the store, never the
// bootstrap file.
func (a *Service) InfisicalValues(ctx context.Context) (map[string]string, error) {
	cfg := infisicalConfig(a.fileSecrets())
	if !cfg.Configured() {
		return nil, errors.New("Infisical is not set up on this AppTown (secrets.env)")
	}
	m, err := a.infisicalFetch(ctx, cfg)
	if m == nil {
		return nil, fmt.Errorf("Infisical could not be reached: %w", err)
	}
	return m, nil
}

// ErrInfisicalNotSetUp is an Infisical store this AppTown has no
// credentials for: its message says what goes in secrets.env.
var ErrInfisicalNotSetUp = errors.New("not set up on this AppTown: INFISICAL_URL, INFISICAL_CLIENT_ID, INFISICAL_CLIENT_SECRET and INFISICAL_PROJECT_ID go in secrets.env")

// AvailableProviders is the set of stores this AppTown can use now, by the
// name their integrations carry as provider: the built-in store always,
// Infisical when its credentials are in secrets.env. Availability is
// configuration, not a live check (CheckInfisical is that).
func (a *Service) AvailableProviders() map[string]bool {
	out := map[string]bool{}
	for _, s := range a.Stores() {
		if s.Available {
			out[s.Name] = true
		}
	}
	return out
}

// CheckInfisical reads Infisical now, through the same fetch the runtime
// uses, so its failure and recovery receipts are written as for any read.
// Unlike a runtime read it answers the fetch's error even when last-known
// values are cached. Not configured is ErrInfisicalNotSetUp.
func (a *Service) CheckInfisical(ctx context.Context) error {
	cfg := infisicalConfig(a.fileSecrets())
	if !cfg.Configured() {
		return ErrInfisicalNotSetUp
	}
	_, err := a.infisicalFetch(ctx, cfg)
	return err
}

// CheckBuiltIn reads the built-in store's key, which every built-in read
// needs; the first use makes it. It says the store's key is readable, not
// that every stored value decrypts.
func (a *Service) CheckBuiltIn(ctx context.Context) error {
	_, err := a.Vault.Key(ctx)
	return err
}
