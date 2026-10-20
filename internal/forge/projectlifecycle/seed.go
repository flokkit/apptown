package projectlifecycle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"
)

// Starting a project from existing code
// (design/operations/start-from-existing-code.md): a new, empty
// project's repository begins with the default branch and tags of a Git
// bundle or an https repository. Nothing else comes with it: the project's
// Changes, runs and chats start fresh. After the seed the repository is
// initialized as any project's is (the push fence, the ignore file, the
// default branch), so packs and toolchains apply as for `apptown new`.

// Seeding is the operator's settings for starting from existing code.
type Seeding struct {
	// AllowPrivate lets the server fetch from private, loopback and
	// link-local addresses (-git-import-allow-private).
	AllowPrivate bool
	// BundleMaxBytes bounds an uploaded bundle (-import-bundle-max-bytes);
	// zero is DefaultBundleMaxBytes.
	BundleMaxBytes int64
}

// DefaultBundleMaxBytes is the upload limit when the operator sets none.
const DefaultBundleMaxBytes int64 = 2 << 30

// Secrets reads the forge's own secrets, for a git credential named by an
// administrator.
type Secrets interface {
	WithContext(ctx context.Context) (map[string]string, error)
}

// BundleTooLarge refuses an upload over the operator's limit.
type BundleTooLarge struct{ Limit int64 }

func (e *BundleTooLarge) Error() string {
	return fmt.Sprintf("the bundle is larger than this AppTown accepts (%d bytes; the operator sets -import-bundle-max-bytes)", e.Limit)
}
func (e *BundleTooLarge) FaultKind() fault.Kind { return fault.Invalid }

// ErrNoSuchSecret refuses a credential name the forge does not hold.
var ErrNoSuchSecret = fault.New(fault.Invalid, "AppTown holds no secret by that name")

// FromURL is a seed from an https repository. CredentialSecret names one of
// the forge's own secrets to offer as the password; the caller has already
// established that the actor is an administrator, since the value goes to
// a server the actor chose.
type FromURL struct {
	URL, Branch, CredentialSecret string
}

// SeedFromURL starts p's repository from an https repository.
func (s *Service) SeedFromURL(ctx context.Context, p *project.Project, in FromURL, who string) error {
	policy := scm.URLPolicy{AllowPrivate: s.seeding.AllowPrivate, Resolve: s.resolve}
	remote, err := policy.Check(ctx, in.URL)
	if err != nil {
		return err
	}
	if in.CredentialSecret != "" {
		values, err := s.secrets.WithContext(ctx)
		if err != nil {
			return fault.Wrap(fault.Unavailable, fmt.Errorf("AppTown's secrets could not be read: %w", err))
		}
		value, ok := values[in.CredentialSecret]
		if !ok || value == "" {
			return ErrNoSuchSecret
		}
		remote.Password = value
	}
	return s.seed(ctx, p, who, scm.ViaSeed, map[string]any{"source": "url", "url": remote.URL, "credentialSecret": in.CredentialSecret}, func(dir string) (string, error) {
		return scm.SeedFromRemote(ctx, dir, remote, in.Branch)
	})
}

// SeedFromBundle starts p's repository from an uploaded bundle, read from
// body to a spool file within the operator's limit.
func (s *Service) SeedFromBundle(ctx context.Context, p *project.Project, body io.Reader, branch, who string) error {
	limit := s.seeding.BundleMaxBytes
	if limit <= 0 {
		limit = DefaultBundleMaxBytes
	}
	spool := filepath.Join(s.dataDir, "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(spool, "bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fault.Wrap(fault.Invalid, fmt.Errorf("the bundle could not be received: %w", err))
	}
	if n > limit {
		return &BundleTooLarge{Limit: limit}
	}
	return s.seed(ctx, p, who, scm.ViaSeed, map[string]any{"source": "bundle", "bytes": n}, func(dir string) (string, error) {
		return scm.SeedFromBundle(ctx, dir, f.Name(), branch)
	})
}

// seed runs fill into p's hub under the project's repository lock, then
// initializes the repository as any project's is (recording its main under
// via) while still holding the lock, and records a receipt.
func (s *Service) seed(ctx context.Context, p *project.Project, who string, via scm.MainVia, source map[string]any, fill func(dir string) (string, error)) error {
	release, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.projects.ByID(ctx, p.ID)
	if err != nil {
		return err
	}
	if current.DefaultBranch != "" {
		return scm.ErrRepositoryEstablished
	}
	dir := filepath.Join(s.dataDir, "workspaces", "project-"+current.Slug)
	branch, err := fill(dir)
	if err != nil {
		return err
	}
	// The hub was empty before fill made it; a setup that fails afterwards
	// takes it away again, so "no repository" stays true and the seed can
	// be tried again.
	if err := s.initializeHeld(ctx, &current, branch, via); err != nil {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return fmt.Errorf("setting up the imported repository failed (%w), and removing it failed too (%v): the history is at %s while the project records no repository; an administrator removes that folder, then the seed can be repeated", err, rmErr, dir)
		}
		return fmt.Errorf("setting up the imported repository failed, so it was removed and the project still has no repository; try again: %w", err)
	}
	*p = current
	source["branch"] = branch
	source["head"] = gitexec.HubRef(ctx, dir, "refs/heads/"+branch)
	audit.At(s.now()).BestEffort(ctx, s.db, who, p.ID, "project.repo.seeded", p.Slug, source)
	return nil
}

// WriteBundle writes p's default branch and tags as a Git bundle: the
// Download code door.
func (s *Service) WriteBundle(ctx context.Context, p *project.Project, w io.Writer) error {
	return scm.WriteBundle(ctx, filepath.Join(s.dataDir, "workspaces", "project-"+p.Slug), p.DefaultBranch, w)
}
