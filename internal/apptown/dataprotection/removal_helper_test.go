package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
)

// Older provider assertions exercise the public admission and worker, too.
func (s *Service) Remove(ctx context.Context, in RemoveDeployment) (removalResult, error) {
	if err := jobs.Migrate(ctx, s.db.Pool()); err != nil {
		return removalResult{}, err
	}
	if s.ops == nil {
		s.ops = operations.New(s.db, s.authority, removalStill{s.authority}, s.now)
		s.registerKinds()
	}
	op, err := s.TakeDown(ctx, in)
	if err != nil {
		return removalResult{}, err
	}
	done, err := s.ops.RunHere(context.WithoutCancel(ctx), op.ID)
	if err != nil {
		return removalResult{}, err
	}
	var out removalResult
	raw, err := json.Marshal(done.Result)
	if err != nil {
		return out, err
	}
	var result struct {
		Deleted, Retained []string
		FinalBackup       interface{}
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return out, err
	}
	out.Deleted, out.Retained = result.Deleted, result.Retained
	if id := inputID(done.Result, "finalBackup"); !id.IsZero() {
		b, err := s.backups.ByID(ctx, in.ProjectID, in.Name, id)
		if err != nil {
			return out, err
		}
		out.FinalBackup = &FinalSet{ID: b.ID, Bytes: b.Bytes}
	}
	if done.State != operations.Succeeded {
		return out, fmt.Errorf("%s: %s", done.State, done.Detail)
	}
	return out, nil
}

type removalStill struct{ authority access.Authorizer }

func (r removalStill) RequireStill(ctx context.Context, _ access.Principal, p access.Permission) error {
	_, err := r.authority.Require(ctx, p)
	return err
}
