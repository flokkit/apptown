package secret

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/fault"
)

// Instance secrets are the credentials AppTown itself uses, named by a
// target or a connector (a cloud API token, an SSH key, a registry password)
// and read by runtime lookup: Infisical's values with the bootstrap file's
// lines over them. SetInstance and RemoveInstance are the write-only door to
// the bootstrap file. Neither returns, logs nor records a value; each leaves
// a receipt naming who acted on which name. Callers authorize the caller.
//
// A name Infisical holds is refused rather than written: a line in the file
// would silently win over Infisical's value. Infisical's own identity
// (INFISICAL_*) configures the store and is not set through this door.

var (
	ErrInstanceName     = fault.New(fault.Invalid, "an instance secret's name is upper case letters, digits and underscores, starting with a letter")
	ErrInstanceValue    = fault.New(fault.Invalid, "an instance secret's value is one non-empty line")
	ErrHeldByInfisical  = fault.New(fault.Conflict, "held in Infisical")
	ErrInfisicalUnknown = fault.New(fault.Unavailable, "Infisical could not be read")
	ErrInstanceAbsent   = fault.New(fault.NotFound, "AppTown's secrets file holds no such secret")
	ErrSecretsUnread    = fault.New(fault.Unavailable, "AppTown's secrets could not be read")
)

// InstanceStore is what the receipts and answers call the bootstrap file.
const InstanceStore = "secrets.env"

// SetInstance writes name=value into the bootstrap file, replacing any line
// for name. The receipt is written in the same transaction as the file is
// replaced; if the commit fails after the file was written, the value is set
// without its receipt and the error says so.
func (a *Service) SetInstance(ctx context.Context, name, value, who string) error {
	if !ValidName(name) || strings.HasPrefix(name, "INFISICAL_") {
		return fmt.Errorf("%w: %q", ErrInstanceName, name)
	}
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\n\r") {
		return ErrInstanceValue
	}
	if err := a.refuseInfisicalName(ctx, name, "a line in AppTown's secrets file would silently win over Infisical's value; change it in Infisical, or remove it there first"); err != nil {
		return err
	}
	return a.instanceReceipt(ctx, who, "secret.instance-set", name, func() (bool, error) {
		return a.rewriteBootstrap(name, &value)
	})
}

// RemoveInstance drops name's line from the bootstrap file. A name the file
// does not hold is not found; when Infisical holds it, the answer says so.
func (a *Service) RemoveInstance(ctx context.Context, name, who string) error {
	if !ValidName(name) || strings.HasPrefix(name, "INFISICAL_") {
		return fmt.Errorf("%w: %q", ErrInstanceName, name)
	}
	file, err := a.readFileSecrets()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSecretsUnread, err)
	}
	if _, held := file[name]; !held {
		if err := a.refuseInfisicalName(ctx, name, "AppTown's secrets file does not hold it; remove it in Infisical"); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrInstanceAbsent, name)
	}
	return a.instanceReceipt(ctx, who, "secret.instance-removed", name, func() (bool, error) {
		had, err := a.rewriteBootstrap(name, nil)
		if err == nil && !had {
			return false, fmt.Errorf("%w: %s", ErrInstanceAbsent, name)
		}
		return had, err
	})
}

// instanceReceipt records a change to the bootstrap file, which runtime
// reads for its credentials and which no transaction covers: the request
// before the rewrite (no request record, no rewrite) and the outcome after,
// <kind> or <kind>-failed. Both name the secret, never its value. An
// outcome that cannot be recorded is an error, though the file was changed.
func (a *Service) instanceReceipt(ctx context.Context, who, kind, name string, change func() (bool, error)) error {
	if err := audit.At(a.now()).Record(ctx, a.Vault.db, who, ids.Nil, kind+"-requested", name, map[string]any{"store": InstanceStore}); err != nil {
		return err
	}
	// The file changes whether or not the caller stays: its outcome is
	// recorded though the caller has gone.
	settle := context.WithoutCancel(ctx)
	had, err := change()
	if err != nil {
		if recErr := audit.At(a.now()).Record(settle, a.Vault.db, who, ids.Nil, kind+"-failed", name, map[string]any{"store": InstanceStore, "error": err.Error()}); recErr != nil {
			return fmt.Errorf("%w; and recording that it failed: %v", err, recErr)
		}
		return err
	}
	if err := audit.At(a.now()).Record(settle, a.Vault.db, who, ids.Nil, kind, name, map[string]any{"store": InstanceStore, "replaced": had && kind == "secret.instance-set"}); err != nil {
		return fmt.Errorf("AppTown's secrets file was changed for %s, but its outcome was not recorded: %w", name, err)
	}
	return nil
}

// refuseInfisicalName refuses a name Infisical holds, and answers unknown
// when Infisical is configured but cannot be read: AppTown cannot tell
// whether the file would shadow it.
func (a *Service) refuseInfisicalName(ctx context.Context, name, why string) error {
	file, err := a.readFileSecrets()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSecretsUnread, err)
	}
	cfg := infisicalConfig(file)
	if !cfg.Configured() {
		return nil
	}
	values, err := a.infisicalFetch(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%w, so AppTown cannot tell whether it holds %s: %w", ErrInfisicalUnknown, name, err)
	}
	if _, held := values[name]; held {
		return fmt.Errorf("%w: %s — %s", ErrHeldByInfisical, name, why)
	}
	return nil
}

// InstanceNames is every instance secret's name with the store runtime
// lookup reads it from: the secrets file, which wins, or Infisical. Infisical's
// own identity (INFISICAL_*) is left out. Values are never returned. Unlike
// runtime lookup this inventory does not fall back: an unreadable secrets
// file, or a configured Infisical whose refresh fails, is an error (unknown),
// never an empty or partial list.
func (a *Service) InstanceNames(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := a.readFileSecrets()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretsUnread, err)
	}
	out := map[string]string{}
	if cfg := infisicalConfig(file); cfg.Configured() {
		remote, err := a.infisicalFetch(ctx, cfg)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("%w: Infisical could not be read: %w", ErrSecretsUnread, err)
		}
		for k := range remote {
			out[k] = StoreInfisical
		}
	}
	for k := range file {
		out[k] = InstanceStore
	}
	for k := range out {
		if strings.HasPrefix(k, "INFISICAL_") {
			delete(out, k)
		}
	}
	return out, nil
}
