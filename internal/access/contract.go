package access

import (
	"context"

	"github.com/flokkit/apptown/internal/fault"
)

// Permission names an operation and the containing resource resolved by its
// owner. Context carries receipt attribution.
type Permission struct {
	Capability Capability
	Resource   Resource
	Context    Context
}

// Authority is the checked Access contract. Consumers declare their own
// matching interface; no consumer receives a store or a permission cache.
type Authority interface {
	Require(context.Context, Principal, Permission) (Principal, error)
}

var ErrPrincipalRequired = fault.New(fault.Unauthenticated, "a verified principal is required")

// Denied is a policy refusal, distinct from a failure to read policy.
type Denied struct{ Code, Reason string }

func (d *Denied) Error() string { return d.Reason }

// FaultKind: a denial is the caller being refused, not a failure.
func (d *Denied) FaultKind() fault.Kind { return fault.Forbidden }

// FaultCode is the denial's stable reason code, for clients that branch on it.
func (d *Denied) FaultCode() string { return d.Code }
