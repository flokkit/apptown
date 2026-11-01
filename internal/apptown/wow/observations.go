package wow

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
)

// BlockRef names catalog metadata, an exact publication, or the publication
// that existed at a historical instant. Zero Version and AsOf request metadata.
type BlockRef struct {
	ID      ids.ID
	Name    string
	Version int
	AsOf    time.Time
}

type BlockObservation struct {
	Block      Block
	Version    int
	Body       string
	ChecksJSON string
}

// Block reads the catalog fact requested by Adoption without choosing a newer
// version when a historical publication is absent.
func (s *Service) Block(ctx context.Context, ref BlockRef) (BlockObservation, error) {
	var out BlockObservation
	var record *Block
	var err error
	if !ref.ID.IsZero() {
		record, err = s.library.byID(ctx, ref.ID)
	} else {
		record, err = s.library.ByName(ctx, ref.Name)
	}
	if err != nil {
		return out, err
	}
	out.Block = *record
	if ref.Version == 0 && ref.AsOf.IsZero() {
		return out, nil
	}
	q := wowsql.New(s.db.SQL())
	var version wowsql.GuidanceBlockVersion
	if !ref.AsOf.IsZero() {
		version, err = q.BlockVersionAsOf(ctx, wowsql.BlockVersionAsOfParams{BlockID: record.ID, CreatedAt: ref.AsOf})
	} else {
		version, err = q.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: record.ID, Version: int64(ref.Version)})
	}
	if err != nil {
		return out, err
	}
	out.Version, out.Body, out.ChecksJSON = int(version.Version), version.BodyMd, string(version.ChecksJson)
	return out, nil
}

// WorkflowRef keeps name resolution separate from publication selection. A
// historical caller resolves an alias before requesting its recorded version.
type WorkflowRef struct {
	Name          string
	Version       int
	ResolveSystem bool
	MetadataOnly  bool
}

type WorkflowObservation struct {
	Name    string
	Origin  string
	Version int
	YAML    string
}

func (s *Service) Workflow(ctx context.Context, ref WorkflowRef) (WorkflowObservation, error) {
	out := WorkflowObservation{Name: ref.Name}
	if ref.ResolveSystem {
		binding, err := wowsql.New(s.db.SQL()).SystemWorkflow(ctx, out.Name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil && binding.Name != "" {
			out.Name = binding.Name
		}
	}
	var err error
	out.Origin, err = s.workflows.origin(ctx, out.Name)
	if ref.MetadataOnly && errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if ref.MetadataOnly {
		return out, nil
	}
	out.Version, out.YAML, err = s.workflows.version(ctx, out.Name, ref.Version)
	return out, err
}

func (s *Service) PackPublication(ctx context.Context, name string, version int64) (PackVersion, error) {
	return PackVersionIn(ctx, s.db, name, version)
}
