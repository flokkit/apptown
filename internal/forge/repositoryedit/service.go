// Package repositoryedit owns versioned application-declaration edits and their
// repository effects. Work governs Change creation; Publisher owns remote pushes.
package repositoryedit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/trailer"
)

type Publisher interface {
	PushBranch(context.Context, ids.ID, string, string)
}

// Genesis commits a new project's first files, refusing once the project has
// a Change (projectlifecycle.Service.CommitGenesis).
type Genesis interface {
	CommitGenesis(ctx context.Context, p *project.Project, message, actor string, write func(dir string) ([]string, error)) ([]string, error)
}
type Service struct {
	work      *workflow.Service
	catalog   *dependency.Catalog
	publisher Publisher
	branches  *scm.Branches
	genesis   Genesis
	db        *database.DB
	dataDir   string
}

// now is the forge's clock, as Work holds it: the times this package
// writes are Work's records'.
func (editor *Service) now() time.Time { return workClock(editor.work)() }

// workClock is Work's clock, or the real one without a Work (a test's).
func workClock(work *workflow.Service) func() time.Time {
	if work == nil {
		return clock.Or(nil)
	}
	return work.Now
}

func New(work *workflow.Service, catalog *dependency.Catalog, publisher Publisher, branches *scm.Branches, genesis Genesis, db *database.DB, dataDir string) *Service {
	return &Service{work: work, catalog: catalog, publisher: publisher, branches: branches, genesis: genesis, db: db, dataDir: dataDir}
}

// NeedSelection preserves dependency parameters verbatim in the contract.
type NeedSelection struct {
	Capability string         `json:"capability"`
	Params     map[string]any `json:"params"`
}

// ServicesEdit sets the needs a project declares. A proposed edit names the
// declaration it started from (DeclarationVersion); a direct seed of a new
// project needs none.
type ServicesEdit struct {
	Needs               []NeedSelection
	Title, Actor        string
	Direct              bool
	ExpectedDeclaration *string
}

// ErrDeclarationVersionRequired refuses a proposed edit that names no start.
var ErrDeclarationVersionRequired = fault.New(fault.Invalid, "send the declaration the edit started from (expectedDeclaration)")

// ErrDeclarationMoved carries a code so a client can tell it from other
// conflicts and show the person the services declared now.
var ErrDeclarationMoved error = declarationMoved{}

type declarationMoved struct{}

func (declarationMoved) Error() string {
	return "the app's services changed on main since this edit started; review them before saving again"
}
func (declarationMoved) FaultKind() fault.Kind { return fault.Conflict }
func (declarationMoved) FaultCode() string     { return "settings-moved" }

// DeclarationError identifies invalid input or an unreadable current declaration.
type DeclarationError struct{ Err error }

func (e *DeclarationError) Error() string         { return e.Err.Error() }
func (e *DeclarationError) FaultKind() fault.Kind { return fault.Unprocessable }
func (e *DeclarationError) Unwrap() error         { return e.Err }

// SetServices seeds a new project directly or commits a proposed Change. A
// direct seed is refused once the project has a Change (the genesis rule).
// The existing separate writes are preserved: an admitted Change survives
// later errors. The Change is nil for a direct seed.
func (editor *Service) SetServices(ctx context.Context, p *project.Project, in ServicesEdit) (*workflow.Change, error) {
	for _, n := range in.Needs {
		if editor.dependencyKind(ctx, n.Capability) == nil {
			return nil, &DeclarationError{fmt.Errorf("need %q is not in the dependency catalog", n.Capability)}
		}
	}
	repo := filepath.Join(editor.dataDir, "workspaces", "project-"+p.Slug)
	if in.Direct {
		_, err := editor.genesis.CommitGenesis(ctx, p, "forge: declare services", in.Actor, func(dir string) ([]string, error) {
			updated, err := editDeclaration(dir, in.Needs)
			if err != nil {
				return nil, &DeclarationError{err}
			}
			if err := scm.WriteInTree(dir, filepath.Join(".forge", "app.yaml"), updated, 0o644); err != nil {
				return nil, err
			}
			return []string{".forge/app.yaml"}, nil
		})
		if err != nil {
			return nil, err
		}
		audit.At(editor.now()).BestEffort(ctx, editor.db, in.Actor, p.ID, "project.services-seed", p.Slug, map[string]any{"needs": needNames(in.Needs)})
		return nil, nil
	}
	if in.ExpectedDeclaration == nil {
		return nil, ErrDeclarationVersionRequired
	}
	// An early refusal on main as it is now, before any Change is opened.
	if now, err := DeclarationVersion(repo); err != nil {
		return nil, err
	} else if now != *in.ExpectedDeclaration {
		return nil, ErrDeclarationMoved
	}
	if _, err := editDeclaration(repo, in.Needs); err != nil {
		return nil, &DeclarationError{err}
	}
	title := in.Title
	if title == "" {
		title = "Update services"
	}
	ch, err := editor.work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "chore", Title: title, Body: "Set the services the app declares it needs."})
	if err != nil {
		return nil, fmt.Errorf("opening the change: %w", err)
	}
	// The editor holds the new Change while it writes the folder and
	// publishes, like any worker; a worker that took it first refuses the
	// edit. Nothing here can know a chat to have ended, so none is assumed.
	holder, release, err := workflow.TakeRequestIn(ctx, editor.db, p.ID, ch.Branch, "services", nil, editor.now())
	if err != nil {
		return nil, fmt.Errorf("writing the services edit to change %s#%d: %w", p.Slug, ch.Number, err)
	}
	defer func() {
		if err := release(context.WithoutCancel(ctx)); err != nil {
			log.Printf("services edit: releasing change %s#%d: %v", p.Slug, ch.Number, err)
		}
	}()
	// The edit is judged and made on the Change's own workspace, the exact base
	// its commit sits on: a landing between the check above and the workspace
	// clone would otherwise be overwritten by bytes computed from older main.
	// If main moved in that window, the
	// Change is abandoned with the reason and the save refused.
	wt := filepath.Join(editor.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if base, err := DeclarationVersion(wt); err != nil {
		return nil, err
	} else if base != *in.ExpectedDeclaration {
		if _, err := editor.work.Abandon(ctx, workflow.StopWork{ChangeID: ch.ID, Reason: "the app's services changed on main while this edit was being opened; nothing was proposed"}); err != nil {
			return nil, fmt.Errorf("%w; closing the unused change %s#%d: %w", ErrDeclarationMoved, p.Slug, ch.Number, err)
		}
		return nil, ErrDeclarationMoved
	}
	updated, err := editDeclaration(wt, in.Needs)
	if err != nil {
		return nil, &DeclarationError{err}
	}
	if err := os.MkdirAll(filepath.Join(wt, ".forge"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(wt, ".forge", "app.yaml"), updated, 0o644); err != nil {
		return nil, err
	}
	if err := scm.StageAll(ctx, wt); err != nil {
		return nil, err
	}
	if out, err := gitexec.Run(ctx, wt, "commit", "-m", trailer.Message("forge: "+title, "", trailer.Line{Key: trailer.ChangeID, Value: ch.ID.String()}, trailer.Line{Key: trailer.Provenance, Value: "service-editor"})); err != nil {
		return nil, fmt.Errorf("commit: %s", out)
	}
	if _, err := editor.branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: repo, Branch: ch.Branch, From: wt, Grant: scm.Grant{Holder: holder}}); err != nil {
		return nil, err
	}
	audit.At(editor.now()).BestEffort(ctx, editor.db, in.Actor, p.ID, "change.services-edit", fmt.Sprintf("change %d", ch.ID), map[string]any{"needs": needNames(in.Needs)})
	return ch, nil
}
func (editor *Service) dependencyKind(ctx context.Context, capability string) *dependency.Kind {
	d, err := editor.catalog.ByCapability(ctx, capability)
	if err != nil {
		if !errors.Is(err, dependency.ErrUnknown) && !errors.Is(err, dependency.ErrSpec) {
			log.Printf("reading the dependency kind %s: %v", capability, err)
		}
		return nil
	}
	return d
}
