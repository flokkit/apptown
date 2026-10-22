package projectlifecycle

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/trailer"
)

// Changes answers whether a project has ever had a Change.
type Changes interface {
	ProjectHasAny(ctx context.Context, projectID ids.ID) (bool, error)
}

// ErrProjectEstablished refuses a direct write to an established project's
// main. Its code lets a client tell it from other conflicts.
var ErrProjectEstablished error = projectEstablished{}

type projectEstablished struct{}

func (projectEstablished) Error() string {
	return "this project already has changes, so its main branch changes only through a change; open one instead"
}
func (projectEstablished) FaultKind() fault.Kind { return fault.Conflict }
func (projectEstablished) FaultCode() string     { return "project-established" }

// CommitGenesis is the one way AppTown writes a project's main outside a
// landing: only while the project has no Change. The wizard's services, the
// pack's starter files and the toolchain door all commit through it, so
// "nothing reaches main except through a Change" has no quiet exception for
// an established project.
//
// The check and the commit run under the repository lock that every Change
// workspace takes before cloning, so a Change opened meanwhile either sees
// this commit or makes the check refuse with ErrProjectEstablished. write
// puts files in the hub's working tree and returns the paths it wrote; only
// those are committed, and a write that writes nothing commits nothing.
// Returns the paths committed.
func (s *Service) CommitGenesis(ctx context.Context, p *project.Project, message, actor string, write func(dir string) ([]string, error)) ([]string, error) {
	if p.DefaultBranch == "" {
		return nil, fault.New(fault.Unprocessable, "project is not repo-backed")
	}
	release, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.requireGenesis(ctx, p.ID); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dataDir, "workspaces", "project-"+p.Slug)
	if !gitexec.IsRepo(dir) {
		return nil, fault.New(fault.Unprocessable, "project is not repo-backed")
	}
	written, err := write(dir)
	if err != nil || len(written) == 0 {
		return nil, err
	}
	if out, err := gitexec.Run(ctx, dir, append([]string{"add", "-f", "--"}, written...)...); err != nil {
		return nil, fmt.Errorf("staging %v: %s", written, out)
	}
	if out, err := gitexec.Run(ctx, dir, "-c", "user.name=apptown", "-c", "user.email=apptown@localhost",
		"commit", "-q", "-m", trailer.Message(message, "", trailer.Line{Key: trailer.Actor, Value: actor})); err != nil {
		return nil, fmt.Errorf("commit: %s", out)
	}
	recordErr := scm.RecordMain(ctx, s.db, dir, p.ID, p.DefaultBranch, scm.ViaGenesis, s.now())
	s.publisher.PushBranch(ctx, p.ID, dir, p.DefaultBranch)
	if recordErr != nil {
		// The commit stands; the older record stays, and the project's main
		// note shows main ahead of it until the next move is recorded.
		return written, fmt.Errorf("committed %v, but recording where main stands failed: %w", written, recordErr)
	}
	return written, nil
}

func (s *Service) requireGenesis(ctx context.Context, projectID ids.ID) error {
	has, err := s.changes.ProjectHasAny(ctx, projectID)
	if err != nil {
		return fmt.Errorf("reading whether the project has changes: %w", err)
	}
	if has {
		return ErrProjectEstablished
	}
	return nil
}
