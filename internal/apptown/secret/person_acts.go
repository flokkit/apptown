package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// BindStored puts a person's value into the built-in store and binds it, as
// one act: the value's new version, the binding and its secret.bound
// receipt commit together. Delivery and Reveal read a binding's latest
// stored version, so a value committed without its binding's receipt would
// already be in force.
func (a *Service) BindStored(ctx context.Context, projectID ids.ID, slug, deployment, name, value, who string) (Binding, error) {
	if value == "" {
		return Binding{}, errEmpty
	}
	sealed, err := a.Vault.seal(ctx, value)
	if err != nil {
		return Binding{}, err
	}
	scope := Scope(slug, deployment, "")
	tx, err := a.Bindings.begin(ctx, projectID)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	version, err := a.Vault.putIn(ctx, tx, scope, name, sealed, who)
	if err != nil {
		return Binding{}, err
	}
	b := Binding{Deployment: deployment, Name: name, Store: StoreInternal, Ref: scope, Version: fmt.Sprint(version), UpdatedBy: who}
	if err := a.Bindings.putIn(ctx, tx, projectID, b); err != nil {
		return Binding{}, err
	}
	return b, tx.Commit()
}

// heldElsewhere is a secret whose value another store holds, which is where
// a person reads it.
type heldElsewhere struct{ Name, Deployment, Store string }

func (e *heldElsewhere) Error() string {
	return fmt.Sprintf("%s in %s is held by %s, which is where to read it", e.Name, e.Deployment, e.Store)
}
func (e *heldElsewhere) FaultKind() fault.Kind { return fault.Conflict }

// revealed is a built-in value a person read, with the version they saw.
type revealed struct {
	Value   string
	Version int
}

// Reveal is the one way a person reads a built-in value: its secret.revealed
// receipt (who, which secret, which deployment and version, never the value)
// is written first, and no receipt, no value. A binding or value that cannot
// be read is the error, never "not bound".
func (a *Service) Reveal(ctx context.Context, projectID ids.ID, deployment, name, who string) (revealed, error) {
	bindings, err := a.Bindings.For(ctx, projectID, deployment)
	if err != nil {
		return revealed{}, err
	}
	b, ok := bindings[name]
	if !ok {
		return revealed{}, ErrBindingMissing
	}
	if b.Store != StoreInternal {
		return revealed{}, &heldElsewhere{Name: name, Deployment: deployment, Store: b.Store}
	}
	value, version, err := a.Vault.Get(ctx, b.Ref, name)
	if errors.Is(err, ErrNotStored) {
		return revealed{}, fault.Wrap(fault.NotFound, err)
	}
	if err != nil {
		return revealed{}, err
	}
	if err := audit.At(a.now()).Record(ctx, a.Vault.db, who, projectID, "secret.revealed", name, map[string]any{"deployment": deployment, "version": version}); err != nil {
		return revealed{}, err
	}
	return revealed{Value: value, Version: version}, nil
}
