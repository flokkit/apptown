package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/secret/internal/secretsql"
)

// The two backing stores (§2). The built-in store is in-process: values
// live in the forge's own database, obfuscated with a key the forge
// generated and keeps in the same database. That protects against
// accidental disclosure, not against someone holding the database.
// Infisical values never touch the forge's database.
const (
	StoreInternal  = "internal"
	StoreInfisical = "infisical"
)

var (
	ErrEmpty          = errors.New("an empty value is not a secret")
	ErrNotStored      = errors.New("no stored value")
	ErrVersionGone    = errors.New("secret version is no longer stored")
	ErrBindingMissing = fault.New(fault.NotFound, "secret binding does not exist")
	ErrMalformed      = errors.New("stored value is malformed")
)

// Scope is where a value lives in the built-in store: a generated value's
// stability domain (§8) or a supplied value's home.
func Scope(slug, deployment, domain string) string {
	if domain == "project" {
		return "project:" + slug
	}
	return "env:" + slug + ":" + deployment
}

// LockValueIn serialises work on one secret's value across database
// clients, including its first write when there is no row to lock. A hash
// collision only serialises unrelated secrets; it cannot mix them.
func LockValueIn(ctx context.Context, tx *database.Tx, scope, name string) error {
	return queries(tx).LockSecretValue(ctx, secretsql.LockSecretValueParams{Column1: scope, Column2: name})
}

// Vault is the built-in store.
type Vault struct {
	db    *database.DB
	clock func() time.Time
}

func (v *Vault) now() time.Time { return clock.Or(v.clock)().UTC() }

// Key is the instance's obfuscation key, minted on first use. Exactly one
// key ever: the insert does nothing when another writer got there first,
// and everyone reads back whichever key won. Minting leaves a receipt.
func (v *Vault) Key(ctx context.Context) ([]byte, error) {
	if h, err := v.setting(ctx); err != nil || h != "" {
		if err != nil {
			return nil, err
		}
		return hex.DecodeString(h)
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	err := v.db.InTx(ctx, func(tx *database.Tx) error {
		n, err := queries(tx).InsertStoreKey(ctx, hex.EncodeToString(k))
		if err != nil {
			return err
		}
		if n != 1 {
			return nil
		}
		return audit.At(v.now()).RecordIn(ctx, tx, "forge-system", ids.Nil, "secrets.store-initialised", "built-in store",
			map[string]any{"note": "obfuscation key generated; values are obfuscated, not encrypted against database access"})
	})
	if err != nil {
		return nil, err
	}
	h, err := v.setting(ctx)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(h)
}

func (v *Vault) setting(ctx context.Context) (string, error) {
	row, err := queries(v.db).StoreKey(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return row.Value, err
}

func (v *Vault) seal(ctx context.Context, plain string) (string, error) {
	gcm, err := v.cipher(ctx)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (v *Vault) open(ctx context.Context, stored string) (string, error) {
	gcm, err := v.cipher(ctx)
	if err != nil {
		return "", err
	}
	return openWith(gcm, stored)
}

// openWith opens a stored value with a cipher already made from the key.
func openWith(gcm cipher.AEAD, stored string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrMalformed
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return string(plain), nil
}

func (v *Vault) cipher(ctx context.Context) (cipher.AEAD, error) {
	key, err := v.Key(ctx)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Put stores a new version of a value under a scope and returns the
// version. The value is never logged; only its name and version are.
func (v *Vault) Put(ctx context.Context, scope, name, value, who string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, ErrEmpty
	}
	sealed, err := v.seal(ctx, value)
	if err != nil {
		return 0, err
	}
	var version int
	err = v.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockValueIn(ctx, tx, scope, name); err != nil {
			return err
		}
		at := v.now()
		setter, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		row, err := queries(tx).InsertSecretValue(ctx, secretsql.InsertSecretValueParams{
			ID: ids.New(at), Scope: scope, Name: name, Value: sealed, CreatedAt: at,
			CreatedBy: who, CreatedByPrincipal: setter.Principal, CreatedByVia: setter.Via,
		})
		version = int(row.Version)
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// Get is the latest version of a value, ErrNotStored when there is none.
func (v *Vault) Get(ctx context.Context, scope, name string) (value string, version int, err error) {
	row, err := queries(v.db).LatestSecretValue(ctx, secretsql.LatestSecretValueParams{Scope: scope, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, fmt.Errorf("%w: %s", ErrNotStored, name)
	}
	if err != nil {
		return "", 0, err
	}
	version = int(row.Version)
	value, err = v.open(ctx, row.Value)
	if err != nil {
		return "", 0, err
	}
	return value, version, nil
}

// Current is every stored secret's current value, opened: what masking
// replaces. A value that cannot be opened is an error, never skipped.
func (v *Vault) Current(ctx context.Context) ([]string, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	sealed, err := queries(v.db).CurrentSecretValues(ctx)
	if err != nil || len(sealed) == 0 {
		return nil, err
	}
	gcm, err := v.cipher(ctx) // the key is read once for every value
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(sealed))
	for _, s := range sealed {
		value, err := openWith(gcm, s)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

// derivedPurpose separates Derived's key from the store's obfuscation key,
// which is never used directly for a second purpose.
const derivedPurpose = "forge need credentials v1"

// Derived is a value made from the store's key for one owner's credential:
// 32 bytes of HMAC-SHA256 over scope and name, as hex. The same scope and
// name always give the same value, and different ones unrelated values;
// nothing is written. It is how a deployment's or a preview's need
// credentials are made (deployment.NeedCredentials): their services set
// them again at every start, so nothing depends on keeping one. Anyone who
// holds the forge's database can derive them, as they can read every
// built-in value: obfuscation, not encryption. A value changes only when
// the store's key does.
func (v *Vault) Derived(ctx context.Context, scope, name string) (string, error) {
	key, err := v.Key(ctx)
	if err != nil {
		return "", err
	}
	purpose := hmac.New(sha256.New, key)
	purpose.Write([]byte(derivedPurpose))
	mac := hmac.New(sha256.New, purpose.Sum(nil))
	mac.Write([]byte(scope))
	mac.Write([]byte{0})
	mac.Write([]byte(name))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Delete removes every version of a value; one never stored is not an
// error. It is for a value the forge minted for something it owns, once
// that owner has gone (an AI models key revoked at take-down).
func (v *Vault) Delete(ctx context.Context, scope, name string) error {
	return v.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockValueIn(ctx, tx, scope, name); err != nil {
			return err
		}
		_, err := queries(tx).DeleteSecretValue(ctx, secretsql.DeleteSecretValueParams{Scope: scope, Name: name})
		return err
	})
}

// Binding: where one secret of one deployment comes from (§5).
type Binding struct {
	Deployment string `json:"deployment"`
	Name       string `json:"name"`
	Store      string `json:"store"`   // internal | infisical
	Ref        string `json:"ref"`     // internal: the scope; infisical: the key
	Version    string `json:"version"` // opaque; internal: the value version
	Generated  bool   `json:"generated"`
	UpdatedAt  string `json:"updatedAt"`
	UpdatedBy  string `json:"updatedBy"`
	// UpdatedByPrincipal is the binder's principal as recorded; zero for the
	// forge, or one not resolved to a person. Put resolves it itself.
	UpdatedByPrincipal ids.ID `json:"updatedByPrincipal,omitzero"`
}

// Bindings is the supported API over secret_bindings. A change takes the
// project lock, then the value's scope lock, so a shared generated value
// is safe while another deployment binds or unbinds it.
type Bindings struct {
	db    *database.DB
	clock func() time.Time
}

func (s *Bindings) now() time.Time { return clock.Or(s.clock)().UTC() }

// For is one deployment's bindings by name.
func (s *Bindings) For(ctx context.Context, projectID ids.ID, deployment string) (map[string]Binding, error) {
	rows, err := queries(s.db).BindingsForDeployment(ctx, secretsql.BindingsForDeploymentParams{
		ProjectID: projectID, Deployment: deployment,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]Binding{}
	for _, r := range rows {
		out[r.Name] = binding(r)
	}
	return out, nil
}

func (s *Bindings) begin(ctx context.Context, projectID ids.ID) (*database.Tx, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := queries(tx).LockProjectForBindings(ctx, projectID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// Put binds a name; a built-in binding must point at a stored version.
func (s *Bindings) Put(ctx context.Context, projectID ids.ID, b Binding) error {
	tx, err := s.begin(ctx, projectID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if b.Store == StoreInternal {
		if err := LockValueIn(ctx, tx, b.Ref, b.Name); err != nil {
			return err
		}
		exists, err := queries(tx).SecretValueVersionExists(ctx, secretsql.SecretValueVersionExistsParams{
			Scope: b.Ref, Name: b.Name, Column3: b.Version,
		})
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: secret %s version %s; set it again", ErrVersionGone, b.Name, b.Version)
		}
	}
	gen := 0
	if b.Generated {
		gen = 1
	}
	binder, err := identity.ActorIn(ctx, tx, b.UpdatedBy)
	if err != nil {
		return err
	}
	if _, err := queries(tx).UpsertBinding(ctx, secretsql.UpsertBindingParams{
		ProjectID: projectID, Deployment: b.Deployment, Name: b.Name, Store: b.Store, Ref: b.Ref,
		Version: b.Version, Generated: int64(gen), UpdatedAt: s.now(), UpdatedBy: b.UpdatedBy,
		UpdatedByPrincipal: binder.Principal, UpdatedByVia: binder.Via,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Unbind removes a binding and, for the built-in store, every stored
// version nothing else binds any more, with the receipt.
func (s *Bindings) Unbind(ctx context.Context, projectID ids.ID, deployment, name, who string) error {
	tx, err := s.begin(ctx, projectID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	held, err := queries(tx).BindingByName(ctx, secretsql.BindingByNameParams{
		ProjectID: projectID, Deployment: deployment, Name: name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBindingMissing
	}
	if err != nil {
		return err
	}
	store, ref := held.Store, held.Ref
	if store == StoreInternal {
		if err := LockValueIn(ctx, tx, ref, name); err != nil {
			return err
		}
	}
	if _, err := queries(tx).DeleteBinding(ctx, secretsql.DeleteBindingParams{
		ProjectID: projectID, Deployment: deployment, Name: name,
	}); err != nil {
		return err
	}
	if store == StoreInternal {
		counts, err := queries(tx).DeleteUnboundSecretValues(ctx, secretsql.DeleteUnboundSecretValuesParams{
			Scope: ref, Name: name,
		})
		if err != nil {
			return err
		}
		if counts.Selected != counts.Removed {
			return fmt.Errorf("secret %s stored versions were not removed", name)
		}
	}
	if err := audit.At(s.now()).RecordIn(ctx, tx, who, projectID, "secret.unbound", name, map[string]any{"deployment": deployment, "store": store}); err != nil {
		return err
	}
	return tx.Commit()
}

// Profiles is a deployment's chosen declared profile.
type Profiles struct{ db *database.DB }

func NewProfiles(db *database.DB) *Profiles { return &Profiles{db: db} }

// Chosen is the explicit choice, empty when none was made.
func (p *Profiles) Chosen(ctx context.Context, projectID ids.ID, deployment string) (string, error) {
	row, err := queries(p.db).ChosenProfile(ctx, secretsql.ChosenProfileParams{ProjectID: projectID, Deployment: deployment})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return row.Profile, err
}

func (p *Profiles) Set(ctx context.Context, projectID ids.ID, deployment, profile string) error {
	return queries(p.db).SetProfile(ctx, secretsql.SetProfileParams{
		ProjectID: projectID, Deployment: deployment, Profile: profile,
	})
}
