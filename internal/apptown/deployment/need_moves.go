package deployment

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/imageline"
)

func (s *Service) storageBindings(ctx context.Context, dep ids.ID) ([]deploy.StorageBinding, error) {
	rows, err := deploymentsql.New(s.db.SQL()).StorageBindings(ctx, dep)
	if err != nil {
		return nil, err
	}
	out := make([]deploy.StorageBinding, 0, len(rows))
	for _, row := range rows {
		b, err := storageBinding(row)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
func (s *Service) recordNeedMoves(ctx context.Context, release, dep ids.ID, before []deploy.StorageBinding) error {
	after, err := s.storageBindings(ctx, dep)
	if err != nil {
		return err
	}
	moves := []NeedMove{}
	for _, a := range after {
		for _, b := range before {
			if a.Need != b.Need || a.DeclaredImage == b.DeclaredImage && a.ImageID == b.ImageID {
				continue
			}
			from, err := imageline.Parse(b.DeclaredImage, imageline.Line)
			if err != nil {
				return err
			}
			to, err := imageline.Parse(a.DeclaredImage, imageline.Line)
			if err != nil {
				return err
			}
			version := from.Prefix
			// The historical floating line records no build version.
			kind, err := s.env.needs.ByCapability(ctx, a.Need)
			if err != nil {
				return err
			}
			line, _ := kind.ImageLine(map[string]string{"version": version})
			parsedLine, err := imageline.Parse(line, imageline.Line)
			if err == nil && version == parsedLine.Prefix {
				version = ""
			}
			moves = append(moves, NeedMove{a.Need, version, to.Prefix})
		}
	}
	raw, err := json.Marshal(moves)
	if err != nil {
		return err
	}
	return deploymentsql.New(s.db.SQL()).RecordNeedMoves(ctx, deploymentsql.RecordNeedMovesParams{ID: release, NeedMoves: raw})
}
