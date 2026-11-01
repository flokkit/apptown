package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// StoreInfo describes a store for the Settings page.
type StoreInfo struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

// Stores lists the stores; a secrets file that cannot be read is an error,
// since it says whether Infisical is set up.
func (a *Service) Stores() ([]StoreInfo, error) {
	file, err := a.readFileSecrets()
	if err != nil {
		return nil, err
	}
	out := []StoreInfo{{
		Name: StoreInternal, Title: "Built-in store", Available: true,
		Note: "Part of AppTown itself: no separate service, nothing to run. Values are kept in AppTown's own database, obfuscated with a key AppTown generated. That stops accidental disclosure; it is not a vault, and anyone holding the database can read the values. For anything that matters, connect a real secret store.",
	}}
	inf := StoreInfo{Name: StoreInfisical, Title: "Infisical", Available: infisicalConfig(file).configured(),
		Note: "A binding names a key in the Infisical project and environment this AppTown is configured for. Values never enter AppTown's database."}
	if !inf.Available {
		inf.Note = "N" + errInfisicalNotSetUp.Error()[1:] + "."
	}
	return append(out, inf), nil
}

// --- the built-in store ------------------------------------------------------
// Application delivery and settings use the built-in Vault through these helpers.

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
	file, err := a.readFileSecrets()
	if err != nil {
		return nil, err
	}
	cfg := infisicalConfig(file)
	if !cfg.configured() {
		return nil, errors.New("Infisical is not set up on this AppTown (secrets.env)")
	}
	m, err := a.infisicalFetch(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("Infisical could not be read: %w", err)
	}
	return m, nil
}

// errInfisicalNotSetUp is an Infisical store this AppTown has no
// credentials for: its message says what goes in secrets.env.
var errInfisicalNotSetUp = errors.New("not set up on this AppTown: INFISICAL_URL, INFISICAL_CLIENT_ID, INFISICAL_CLIENT_SECRET and INFISICAL_PROJECT_ID go in secrets.env")

// AvailableProviders is the set of stores this AppTown can use now, by the
// name their integrations carry as provider: the built-in store always,
// Infisical when its credentials are in secrets.env. Availability is
// configuration, not a live check (CheckInfisical is that). A secrets
// file that cannot be read is an error.
func (a *Service) AvailableProviders() (map[string]bool, error) {
	stores, err := a.Stores()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range stores {
		if s.Available {
			out[s.Name] = true
		}
	}
	return out, nil
}

// CheckInfisical reads Infisical now, through the same fetch the runtime
// uses, so its failure and recovery receipts are written as for any read.
// Not configured is ErrInfisicalNotSetUp.
func (a *Service) CheckInfisical(ctx context.Context) error {
	file, err := a.readFileSecrets()
	if err != nil {
		return err
	}
	cfg := infisicalConfig(file)
	if !cfg.configured() {
		return errInfisicalNotSetUp
	}
	_, err = a.infisicalFetch(ctx, cfg)
	return err
}

// CheckBuiltIn reads the built-in store's key, which every built-in read
// needs; the first use makes it. It says the store's key is readable, not
// that every stored value decrypts.
func (a *Service) CheckBuiltIn(ctx context.Context) error {
	_, err := a.Vault.key(ctx)
	return err
}
