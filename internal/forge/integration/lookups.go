package integration

import (
	"context"
	"sort"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
)

// NamesByProvider filters the small binding catalog, preserving name order.
func (s *Bindings) NamesByProvider(ctx context.Context, capability, provider string) ([]string, error) {
	rows, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range rows {
		if r.Capability == capability && r.Provider == provider {
			out = append(out, r.Name)
		}
	}
	return out, nil
}

// FirstName is the first binding of a capability by name.
func (s *Bindings) FirstName(ctx context.Context, capability string) (string, bool, error) {
	r, found, err := s.FirstNamed(ctx, capability, "")
	return r.Name, found, err
}

// FirstNamed selects the requested name or the first name of its capability.
func (s *Bindings) FirstNamed(ctx context.Context, capability, name string) (Binding, bool, error) {
	rows, err := integrationsql.New(s.db.SQL()).Bindings(ctx)
	if err != nil {
		return Binding{}, false, err
	}
	for _, r := range rows {
		if r.Capability == capability && (name == "" || r.Name == name) {
			return bindingRecord(r), true, nil
		}
	}
	return Binding{}, false, nil
}

// CapabilitiesForShareIn keeps bindings stable while a caller prepares its
// policy decision. Locks are acquired by identity in the established order.
func CapabilitiesForShareIn(ctx context.Context, q database.Queryer) ([]string, error) {
	rows, err := integrationsql.New(q.SQL()).BindingsForShare(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Capability)
	}
	return out, nil
}

func (s *Bindings) Names(ctx context.Context, capability string) ([]string, error) {
	bindings, err := ForCapability(ctx, s.db, capability)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	sort.Strings(names)
	return names, nil
}
