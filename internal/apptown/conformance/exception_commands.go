package conformance

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
)

var errInvalidException = fault.New(fault.Invalid, "invalid exception")

type ExceptionRequest struct {
	ProjectID                        ids.ID
	Fingerprint, Identifier, Package string
	Kind                             DispositionKind
	Justification                    string
	ExpiresAt                        stamp.Time
}

func (s *Service) CreateDisposition(ctx context.Context, in ExceptionRequest) (*Disposition, error) {
	who, err := s.require(ctx, permission{Action: createException, ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	in.Fingerprint = strings.TrimSpace(in.Fingerprint)
	in.Identifier = strings.TrimSpace(in.Identifier)
	in.Package = strings.TrimSpace(in.Package)
	in.Justification = strings.TrimSpace(in.Justification)
	if in.ProjectID.IsZero() || !in.Kind.valid() || in.Justification == "" || (in.Fingerprint == "" && in.Identifier == "") {
		return nil, fmt.Errorf("%w: project, kind, justification and finding identity are required", errInvalidException)
	}
	return database.Transact(ctx, s.records.db, func(tx *database.Tx) (*Disposition, error) {
		if err := lockConformanceProject(ctx, tx, in.ProjectID); err != nil {
			return nil, err
		}
		at := s.records.now()
		decider, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return nil, err
		}
		row, err := queries(tx).InsertDisposition(ctx, conformancesql.InsertDispositionParams{ID: ids.New(stamp.Of(at).Time),
			ProjectID: in.ProjectID, Fingerprint: in.Fingerprint, Identifier: in.Identifier, Package: in.Package,
			Status: in.Kind.String(), Justification: in.Justification, By: who, ByPrincipal: decider.Principal, ByVia: decider.Via,
			CreatedAt: stamp.Of(at).Time, ExpiresAt: in.ExpiresAt.NullTime,
		})
		if err != nil {
			return nil, err
		}
		stored, err := disposition(row)
		if err != nil {
			return nil, err
		}
		saved := &stored
		receipt := exceptionCreated{Fingerprint: saved.Fingerprint, Identifier: saved.Identifier, Package: saved.Package, Status: saved.Status, Justification: saved.Justification, ExpiresAt: saved.ExpiresAt}
		if err := audit.At(at).RecordIn(ctx, tx, who, saved.ProjectID, "disposition.created", fmt.Sprint(saved.ID), receipt); err != nil {
			return nil, err
		}
		return saved, nil
	})
}

func readDisposition(ctx context.Context, db database.Queryer, id ids.ID) (*Disposition, error) {
	row, err := queries(db).DispositionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	record, err := disposition(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Service) Disposition(ctx context.Context, id ids.ID) (*Disposition, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	record, err := readDisposition(ctx, s.records.db, id)
	if err != nil {
		return nil, database.Missing(err)
	}
	if _, err := s.require(ctx, permission{Action: readProject, ProjectID: record.ProjectID}); err != nil {
		return nil, err
	}
	return record, nil
}

type revocation struct {
	ID, ProjectID ids.ID
	By            string
}

func (s *Service) RevokeDisposition(ctx context.Context, id ids.ID) (*revocation, error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	record, err := readDisposition(ctx, s.records.db, id)
	if err != nil {
		return nil, err
	}
	who, err := s.require(ctx, permission{Action: revokeException, ProjectID: record.ProjectID})
	if err != nil {
		return nil, err
	}
	return database.Transact(ctx, s.records.db, func(tx *database.Tx) (*revocation, error) {
		if err := lockConformanceProject(ctx, tx, record.ProjectID); err != nil {
			return nil, err
		}
		row, err := queries(tx).DeleteDisposition(ctx, conformancesql.DeleteDispositionParams{ID: id, ProjectID: record.ProjectID})
		if err != nil {
			return nil, err
		}
		saved, err := disposition(row)
		if err != nil {
			return nil, err
		}
		if err := audit.At(s.records.now()).RecordIn(ctx, tx, who, saved.ProjectID, "disposition.revoked", fmt.Sprint(saved.ID), exceptionRevoked{Identifier: saved.Identifier, Fingerprint: saved.Fingerprint}); err != nil {
			return nil, err
		}
		return &revocation{ID: saved.ID, ProjectID: saved.ProjectID, By: who}, nil
	})
}

type exceptionCreated struct {
	Fingerprint   string          `json:"fingerprint"`
	Identifier    string          `json:"identifier"`
	Package       string          `json:"package"`
	Status        DispositionKind `json:"status"`
	Justification string          `json:"justification"`
	ExpiresAt     stamp.Time      `json:"expiresAt"`
}
type exceptionRevoked struct {
	Identifier  string `json:"identifier"`
	Fingerprint string `json:"fingerprint"`
}
