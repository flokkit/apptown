package dataprotection

import (
	"context"
	"errors"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// CannotCopy says why AppTown cannot copy the data a deployment holds
// now, "" when it can or when it holds none (design/subsystems/deployment.md,
// "Releasing over data AppTown cannot copy"). A release onto a deployment whose data AppTown cannot
// copy could change that data with no copy to go back to, so Deployment asks
// before it admits one (deployment.DataCopyCheck). One that has served no
// release holds nothing yet. Whether its data can be copied is what each
// copy itself would find: the held-data plan read and every item in it
// copyable, and on App Platform a backup store and a copy runner.
func (s *core) CannotCopy(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) string {
	if dep.ObservedGeneration == 0 {
		return ""
	}
	if managedProvider(t) != "" {
		items, err := s.managedList(ctx, dep, t)
		if err != nil {
			return err.Error()
		}
		if len(items) == 0 {
			return ""
		}
		if why := unresolvedOf(items); why != "" {
			return why
		}
		if err := s.ready(ctx, t, items); err != nil {
			return err.Error()
		}
		return ""
	}
	if t.HoldsBackups() != nil {
		return "" // a bucket website holds no data
	}
	held, err := s.heldData(ctx, dep, t)
	switch {
	case errors.Is(err, ErrNothingToBackUp), errors.Is(err, ErrHeldDataAbsent):
		return ""
	case err != nil:
		return err.Error()
	}
	return unresolvedOf(held.items)
}

// unresolvedOf names each item AppTown cannot copy, "" when there is none.
func unresolvedOf(items []backupPlanItem) string {
	var why []string
	for _, it := range items {
		if it.Unresolved != "" {
			why = append(why, strings.TrimPrefix(it.Unresolved, "not copied: "))
		}
	}
	return strings.Join(why, "; ")
}
