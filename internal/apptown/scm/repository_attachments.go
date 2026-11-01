package scm

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
)

var ErrNoUpstream = fault.New(fault.NotFound, "project has no upstream repository")

// RepositoryAttachments owns provider identity and remote changes. Its locks
// are the same project repository locks used by publication and deletion.
type RepositoryAttachments struct {
	db        *database.DB
	projects  *project.Projects
	authority access.Checked
	provider  repositoryProvider
	locks     *RepositoryLocks
	dataDir   string
	clock     func() time.Time
}

type AttachmentDependencies struct {
	Projects  *project.Projects
	Authority access.Checked
	Provider  repositoryProvider
	Locks     *RepositoryLocks
	DataDir   string
	Now       func() time.Time
}

func NewRepositoryAttachments(db *database.DB, deps AttachmentDependencies) *RepositoryAttachments {
	return &RepositoryAttachments{db: db, projects: deps.Projects, authority: deps.Authority, provider: deps.Provider, locks: deps.Locks, dataDir: deps.DataDir, clock: deps.Now}
}
func (s *RepositoryAttachments) now() time.Time { return clock.Or(s.clock)().UTC() }

func (s *RepositoryAttachments) snapshot(ctx context.Context, expected *ProviderScope) (ProviderSession, error) {
	if s.provider == nil {
		return ProviderSession{}, fault.New(fault.Unavailable, "repository provider is unavailable")
	}
	if expected != nil {
		if err := expected.validate(); err != nil {
			return ProviderSession{}, err
		}
	}
	session, err := s.provider.Snapshot(ctx, expected)
	if err != nil {
		return ProviderSession{}, err
	}
	if err := session.Scope.validate(); err != nil {
		return ProviderSession{}, err
	}
	if session.Client == nil {
		return ProviderSession{}, fault.New(fault.Unavailable, "repository provider has no credential snapshot")
	}
	base, err := gitlab.CanonicalBase(session.Client.BaseURL())
	if err != nil || base != session.Scope.Base {
		return ProviderSession{}, fault.New(fault.Conflict, "repository client does not match its provider scope")
	}
	if expected != nil && session.Scope != *expected {
		return ProviderSession{}, fault.New(fault.Conflict, "repository provider scope changed; inspect the recorded attachment before continuing")
	}
	return session, nil
}

func observeRepository(session ProviderSession, ref gitlab.ProjectRef) (gitlab.ProjectRef, error) {
	location, path, err := gitlab.RepositoryURL(session.Scope.Base, ref.HTTPURL)
	if err != nil {
		return gitlab.ProjectRef{}, err
	}
	if ref.ID <= 0 || path != ref.PathNS {
		return gitlab.ProjectRef{}, fault.New(fault.Conflict, "provider did not return a coherent repository identity")
	}
	ref.HTTPURL = location
	return ref, nil
}

// VerifiedRepository retains the client that observed its scope. Consumers do
// not reread secrets between identity verification and the admitted effect.
type VerifiedRepository struct {
	Identity RepositoryIdentity
	Provider ProviderSession
	Observed gitlab.ProjectRef
}

// VerifyRecorded is for an already proven MR/token/deletion identity, including
// after the project row is gone. A bare historical path or numeric ID is not
// enough to call it. It does not adopt the current attachment's identity.
func (s *RepositoryAttachments) VerifyRecorded(ctx context.Context, identity RepositoryIdentity) (VerifiedRepository, error) {
	if err := identity.validate(); err != nil {
		return VerifiedRepository{}, err
	}
	session, err := s.snapshot(ctx, &identity.Scope)
	if err != nil {
		return VerifiedRepository{}, err
	}
	observed, err := session.Client.Project(ctx, strconv.FormatInt(identity.ProviderProjectID, 10))
	if err != nil {
		return VerifiedRepository{}, err
	}
	observed, err = observeRepository(session, observed)
	if err != nil {
		return VerifiedRepository{}, err
	}
	if observed.ID != identity.ProviderProjectID {
		return VerifiedRepository{}, fault.New(fault.Conflict, "provider answered with a different repository identity")
	}
	return VerifiedRepository{Identity: identity, Provider: session, Observed: observed}, nil
}

// Resolve observes an existing origin without requiring an eager network
// migration. It records only current attachment identity, never historical MR,
// token or cleanup provenance. Missing and unreadable remain different errors.
func (s *RepositoryAttachments) Resolve(ctx context.Context, projectID ids.ID) (VerifiedRepository, error) {
	release, err := s.locks.Acquire(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	defer release()
	return s.resolveLocked(ctx, projectID, false)
}

// ResolveForDeletion observes the copy a deleted project currently owns before
// purge removes its repository. It grants no push or other provider mutation.
func (s *RepositoryAttachments) ResolveForDeletion(ctx context.Context, projectID ids.ID) (VerifiedRepository, error) {
	release, err := s.locks.Acquire(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	defer release()
	return s.resolveLocked(ctx, projectID, true)
}

func (s *RepositoryAttachments) resolveLocked(ctx context.Context, projectID ids.ID, deleted bool) (VerifiedRepository, error) {
	var check error
	if deleted {
		l, err := project.LifecycleIn(ctx, s.db, projectID)
		check = err
		if err == nil && l.State != project.Deleted {
			check = fault.New(fault.Conflict, "repository deletion identity requires a deleted project")
		}
	} else {
		check = project.RefuseUnlessActive(ctx, s.db, projectID)
	}
	if check != nil {
		return VerifiedRepository{}, check
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	repo, err := p.Repository(s.dataDir)
	if err != nil {
		return VerifiedRepository{}, err
	}
	origin, err := readOrigin(ctx, repo)
	if err != nil {
		return VerifiedRepository{}, err
	}
	identity, found, err := readAttachment(ctx, s.db, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	if found {
		if origin == "" {
			return VerifiedRepository{}, fault.New(fault.Conflict, "verified repository attachment has no local origin; inspect it before continuing")
		}
		verified, err := s.VerifyRecorded(ctx, identity)
		if err != nil {
			return VerifiedRepository{}, err
		}
		location, _, err := gitlab.RepositoryURL(identity.Scope.Base, origin)
		if err != nil || location != identity.CloneURL || verified.Observed.HTTPURL != identity.CloneURL {
			return VerifiedRepository{}, fault.New(fault.Conflict, "repository origin or provider location differs from its attachment; inspect it before using credentials")
		}
		return verified, nil
	}
	intent, found, err := s.readIntent(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	if found {
		return VerifiedRepository{}, intent.refusal()
	}
	if origin == "" {
		return VerifiedRepository{}, ErrNoUpstream
	}
	session, err := s.snapshot(ctx, nil)
	if err != nil {
		return VerifiedRepository{}, err
	}
	location, path, err := gitlab.RepositoryURL(session.Scope.Base, origin)
	if err != nil {
		return VerifiedRepository{}, err
	}
	observed, err := session.Client.Project(ctx, path)
	if err != nil {
		return VerifiedRepository{}, err
	}
	observed, err = observeRepository(session, observed)
	if err != nil {
		return VerifiedRepository{}, err
	}
	if observed.HTTPURL != location {
		return VerifiedRepository{}, fault.New(fault.Conflict, "origin does not match the provider's observed clone URL")
	}
	identity = RepositoryIdentity{AttachmentID: ids.New(s.now()), ProjectID: projectID, Scope: session.Scope, ProviderProjectID: observed.ID, CloneURL: observed.HTTPURL, Path: observed.PathNS}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if deleted {
			l, err := project.LockedLifecycleIn(ctx, tx, projectID)
			if err != nil {
				return err
			}
			if l.State != project.Deleted {
				return fault.New(fault.Conflict, "project deletion state changed during repository observation")
			}
		} else if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		if err := recordAttachmentIn(ctx, tx, identity, s.now()); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, "apptown-system", projectID, "repository.attachment.verified", identity.AttachmentID.String(), map[string]any{"identity": identity})
	})
	if err != nil {
		return VerifiedRepository{}, err
	}
	return VerifiedRepository{Identity: identity, Provider: session, Observed: observed}, nil
}

// Push serializes scope/origin observation with the credential-bearing push.
// Callers finish their local publication and release its repository lock first.
func (s *RepositoryAttachments) Push(ctx context.Context, projectID ids.ID, branch string) (VerifiedRepository, error) {
	release, err := s.locks.Acquire(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	defer release()
	verified, err := s.resolveLocked(ctx, projectID, false)
	if err != nil {
		return VerifiedRepository{}, err
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return VerifiedRepository{}, err
	}
	repo, err := p.Repository(s.dataDir)
	if err != nil {
		return VerifiedRepository{}, err
	}
	env, err := verified.Provider.Client.AskpassEnv(s.dataDir)
	if err != nil {
		return VerifiedRepository{}, err
	}
	destination, err := credentialURL(verified.Identity.CloneURL)
	if err != nil {
		return VerifiedRepository{}, err
	}
	if out, err := gitexec.PushVerifiedURL(ctx, repo, destination, branch, env); err != nil {
		return VerifiedRepository{}, fmt.Errorf("pushing the verified attachment: %w: %s", err, out)
	}
	return verified, nil
}
