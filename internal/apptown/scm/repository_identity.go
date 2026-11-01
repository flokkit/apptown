package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
)

// ProviderScope is the immutable namespace in which external numeric IDs have
// meaning. A credential may rotate within this scope; a connector/host/account
// replacement is not an implicit repository rebind.
type ProviderScope struct {
	ConnectionID ids.ID `json:"connectionId"`
	Kind         string `json:"kind"`
	Base         string `json:"base"`
	AccountID    int64  `json:"accountId"`
}

func (s ProviderScope) validate() error {
	base, err := gitlab.CanonicalBase(s.Base)
	if err != nil || base != s.Base || s.ConnectionID.IsZero() || s.Kind != "gitlab" || s.AccountID <= 0 {
		return fault.New(fault.Conflict, "repository provider identity is incomplete; inspect its attachment")
	}
	return nil
}

// RepositoryIdentity is a recorded attachment generation, suitable for an MR,
// token owner or retained cleanup to keep independently of the project row.
type RepositoryIdentity struct {
	AttachmentID      ids.ID        `json:"attachmentId"`
	ProjectID         ids.ID        `json:"projectId"`
	Scope             ProviderScope `json:"scope"`
	ProviderProjectID int64         `json:"providerProjectId"`
	CloneURL          string        `json:"cloneUrl"`
	Path              string        `json:"path"`
}

func (i RepositoryIdentity) validate() error {
	if err := i.Scope.validate(); err != nil {
		return err
	}
	location, path, err := gitlab.RepositoryURL(i.Scope.Base, i.CloneURL)
	if err != nil || location != i.CloneURL || path != i.Path || i.AttachmentID.IsZero() || i.ProjectID.IsZero() || i.ProviderProjectID <= 0 {
		return fault.New(fault.Conflict, "repository attachment identity is incomplete; inspect its recorded evidence")
	}
	return nil
}

// ProviderSession is one resolved credential snapshot. A provider adapter must
// compare a requested scope's local base/connector before an authenticated
// account probe, then compare the observed account before returning this client.
type ProviderSession struct {
	Scope            ProviderScope
	Client           *gitlab.Client
	DefaultNamespace int
}

type repositoryProvider interface {
	Snapshot(context.Context, *ProviderScope) (ProviderSession, error)
}

func attachmentIdentity(row scmsql.RepositoryAttachment) (RepositoryIdentity, error) {
	i := RepositoryIdentity{AttachmentID: row.ID, ProjectID: row.ProjectID, Scope: ProviderScope{ConnectionID: row.ConnectionID, Kind: row.ProviderKind, Base: row.ProviderBase, AccountID: row.ProviderAccount}, ProviderProjectID: row.ProviderProject, CloneURL: row.CloneUrl, Path: row.ProjectPath}
	return i, i.validate()
}

func readAttachment(ctx context.Context, q database.Queryer, projectID ids.ID) (RepositoryIdentity, bool, error) {
	row, err := scmsql.New(q.SQL()).RepositoryAttachment(ctx, projectID)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return RepositoryIdentity{}, false, err
	}
	identity, err := attachmentIdentity(row)
	return identity, true, err
}

func recordAttachmentIn(ctx context.Context, tx *database.Tx, identity RepositoryIdentity, at time.Time) error {
	if err := identity.validate(); err != nil {
		return err
	}
	n, err := scmsql.New(tx.SQL()).RecordRepositoryAttachment(ctx, scmsql.RecordRepositoryAttachmentParams{ID: identity.AttachmentID, ProjectID: identity.ProjectID, ConnectionID: identity.Scope.ConnectionID, ProviderBase: identity.Scope.Base, ProviderAccount: identity.Scope.AccountID, ProviderProject: identity.ProviderProjectID, CloneUrl: identity.CloneURL, ProjectPath: identity.Path, VerifiedAt: at.UTC()})
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	stored, found, err := readAttachment(ctx, tx, identity.ProjectID)
	if err != nil {
		return err
	}
	if !found || stored != identity {
		return fault.New(fault.Conflict, "repository attachment changed; inspect it before continuing")
	}
	return nil
}

// RecordMergeRequestIdentityIn participates in the artifact's own transaction.
// A path-only historical artifact has no implied association to today's object.
func RecordMergeRequestIdentityIn(ctx context.Context, tx *database.Tx, artifactID ids.ID, identity RepositoryIdentity) error {
	if artifactID.IsZero() {
		return fmt.Errorf("merge-request identity needs its artifact")
	}
	if err := identity.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return scmsql.New(tx.SQL()).RecordMergeRequestIdentity(ctx, scmsql.RecordMergeRequestIdentityParams{ArtifactID: artifactID, IdentityJson: raw})
}

// DecodeRepositoryIdentity is used when the workflow's batched source read
// carries the original MR provenance. Absence remains unverified legacy state.
func DecodeRepositoryIdentity(raw []byte) (*RepositoryIdentity, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var identity RepositoryIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		return nil, fmt.Errorf("unreadable recorded repository identity: %w", err)
	}
	if err := identity.validate(); err != nil {
		return nil, err
	}
	return &identity, nil
}
