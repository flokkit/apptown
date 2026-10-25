// Package datalayout coordinates an offline installation layout transition.
// It runs before application construction, recovery, or serving requests.
package datalayout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

// Observers are read-only views of the existing session runtimes. Supplying no
// runtime is not proof a surviving writer disappeared.
type Observers struct {
	Local, Remote console.Agent
	Tasks         execution.TaskDriver
	TaskBackend   string
}

type publicationEdit struct {
	Before scm.PublicationLocation `json:"before"`
	After  string                  `json:"after"`
}
type mainEvidence struct {
	ProjectID ids.ID `json:"projectId"`
	Branch    string `json:"branch"`
	SHA       string `json:"sha"`
}
type inventory struct {
	Mappings     []repositorylayout.Mapping
	References   []repositorylayout.Reference
	Publications []publicationEdit
	Consoles     []console.CheckoutEdit
	Linked       []repositorylayout.LinkedWorkspace
	Borrowed     []repositorylayout.BorrowedWorkspace
	Mains        []mainEvidence
	Git          []gitexec.ConfigRelocation
}

func inspect(ctx context.Context, db *database.DB, root, oldRoot string, sourceUUID bool, observers Observers) (inventory, error) {
	var out inventory
	owners, err := project.RepositoryOwnersIn(ctx, db)
	if err != nil {
		return out, err
	}
	known := map[ids.ID]project.RepositoryOwner{}
	for _, owner := range owners {
		if owner.Ref.ID.IsZero() || !project.ValidSlug(owner.Ref.Slug) {
			return out, fmt.Errorf("invalid repository owner %s", owner.Ref.ID)
		}
		if _, exists := known[owner.Ref.ID]; exists {
			return out, fmt.Errorf("duplicate repository owner %s", owner.Ref.ID)
		}
		known[owner.Ref.ID] = owner
		main, recorded, err := scm.ReadMain(ctx, db, owner.Ref.ID)
		if err != nil {
			return out, err
		}
		mapping := repositorylayout.Mapping{ID: owner.Ref.ID, Slug: owner.Ref.Slug, SourceUUID: sourceUUID, Missing: missingRepositoryAllowed(owner, recorded)}
		out.Mappings = append(out.Mappings, mapping)
		out.References = append(out.References, mapping.Reference(oldRoot, root))
		hub := mapping.Source(root)
		info, err := os.Lstat(hub)
		if errors.Is(err, fs.ErrNotExist) && mapping.Missing {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("project %s repository: %w", owner.Ref.ID, err)
		}
		if !info.IsDir() {
			return out, fmt.Errorf("project %s repository is not a directory", owner.Ref.ID)
		}
		branch := owner.DefaultBranch
		if recorded {
			branch = main.Branch
		}
		if branch != "" {
			head, err := gitexec.HubRun(ctx, hub, "rev-parse", "--verify", "refs/heads/"+branch)
			if err != nil {
				return out, fmt.Errorf("reading project %s main before relocation: %w", owner.Ref.ID, err)
			}
			if recorded {
				ancestor, err := gitexec.HubIsAncestor(ctx, hub, main.SHA, head)
				if err != nil || !ancestor {
					return out, fmt.Errorf("project %s recorded main does not match its repository history: %v", owner.Ref.ID, err)
				}
			}
			out.Mains = append(out.Mains, mainEvidence{ProjectID: owner.Ref.ID, Branch: branch, SHA: head})
		}
	}

	observed, err := project.RepositoriesOnDisk(root)
	if err != nil {
		return out, err
	}
	identified := map[string]bool{}
	for _, mapping := range out.Mappings {
		identified[mapping.Source(root)] = true
		identified[mapping.Reference(root, root).After] = true
	}
	for _, path := range observed {
		if !identified[path] {
			log.Printf("data layout: preserving unowned repository directory %s", path)
		}
	}
	workspaceRefs, err := workflow.LayoutWorkspaceReferences(ctx, db, oldRoot, root)
	if err != nil {
		return out, err
	}
	out.References = append(out.References, workspaceRefs...)
	publications, err := scm.PublicationLocationsIn(ctx, db)
	if err != nil {
		return out, err
	}
	for _, pub := range publications {
		owner, ok := known[pub.ProjectID]
		if !ok || owner.Retained {
			return out, fmt.Errorf("publication %s has no current repository owner", pub.ID)
		}
		expected := repositorylayout.Mapping{ID: owner.Ref.ID, Slug: owner.Ref.Slug, SourceUUID: sourceUUID}.Reference(oldRoot, root)
		if pub.Repository != expected.Before {
			return out, fmt.Errorf("publication %s points outside its project's original hub", pub.ID)
		}
		out.Publications = append(out.Publications, publicationEdit{Before: pub, After: expected.After})
	}
	requests, err := workflowexec.RelocationRequests(ctx, db, root)
	if err != nil {
		return out, err
	}
	for _, request := range requests {
		if err := request.RefuseMovedPaths(out.References, oldRoot, root); err != nil {
			return out, err
		}
		if err := request.RefuseWriter(ctx, observers.Tasks, observers.TaskBackend); err != nil {
			return out, err
		}
	}
	records, err := console.RelocationRecords(root)
	if err != nil {
		return out, err
	}
	gitRoots := append([]repositorylayout.Reference{}, workspaceRefs...)
	ownedCheckouts := map[string]string{}
	for _, record := range records {
		if conversation.Imported(record.Meta.Labels) {
			continue
		}
		owner, err := ids.Parse(record.Meta.Label(conversation.ProjectLabel))
		if err != nil || owner.IsZero() {
			return out, fmt.Errorf("console %s has no identified project for relocation", record.Meta.ID)
		}
		if _, ok := known[owner]; !ok {
			return out, fmt.Errorf("console %s has an unknown project owner", record.Meta.ID)
		}
		// Project consoles own checkout-<random> snapshots beside session
		// directories. The authoritative record names that one snapshot;
		// two records claiming the same owned checkout are ambiguous.
		refs := append([]repositorylayout.Reference{}, out.References...)
		if record.Meta.OwnsCheckout {
			checkout := record.Meta.Checkout
			if filepath.Clean(checkout) != checkout || filepath.Dir(checkout) != filepath.Join(oldRoot, "console") || !strings.HasPrefix(filepath.Base(checkout), "checkout-") {
				return out, fmt.Errorf("console %s has an invalid owned checkout", record.Meta.ID)
			}
			if earlier, exists := ownedCheckouts[checkout]; exists && earlier != record.Meta.ID {
				return out, fmt.Errorf("consoles %s and %s both own one checkout", earlier, record.Meta.ID)
			}
			snapshot := repositorylayout.Reference{Owner: owner, Before: checkout, After: filepath.Join(root, "console", filepath.Base(checkout))}
			if _, exists := ownedCheckouts[checkout]; !exists {
				gitRoots = append(gitRoots, snapshot)
			}
			ownedCheckouts[checkout] = record.Meta.ID
			refs = append(refs, snapshot)
		}

		after, matched, err := repositorylayout.Translate(refs, owner, record.Meta.Checkout)
		if err != nil {
			return out, err
		}
		if !matched {
			return out, fmt.Errorf("console %s checkout has no identified local owner", record.Meta.ID)
		}
		agent := observers.Local
		if conversation.OnWorkerRecord(record.Meta) {
			agent = observers.Remote
		}
		if err := record.RefuseWriter(ctx, agent); err != nil {
			return out, err
		}
		if after != record.Meta.Checkout {
			edit, err := record.CheckoutEdit(after)
			if err != nil {
				return out, err
			}
			out.Consoles = append(out.Consoles, edit)
		}
	}

	for _, reference := range gitRoots {
		folder := reference.After
		info, err := os.Lstat(filepath.Join(folder, ".git"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		owner, ok := known[reference.Owner]
		if !ok {
			return out, fmt.Errorf("workspace has unknown project %s", reference.Owner)
		}
		hub := repositorylayout.Mapping{ID: owner.Ref.ID, Slug: owner.Ref.Slug, SourceUUID: sourceUUID}.Source(root)
		if !info.IsDir() {
			linked, ok, err := repositorylayout.InspectRestoredLinkedWorkspace(folder, hub, oldRoot, root)
			if err != nil {
				return out, err
			}
			if !ok {
				return out, fmt.Errorf("workspace metadata cannot be classified: %s", folder)
			}
			if err := gitexec.CheckLinkedConfig(ctx, hub); err != nil {
				return out, err
			}
			out.Linked = append(out.Linked, linked)
			continue
		}
		borrowed, borrows, err := repositorylayout.InspectBorrowedWorkspace(folder, hub, oldRoot, root)
		if err != nil {
			return out, err
		}
		if borrows {
			out.Borrowed = append(out.Borrowed, borrowed)
		}
		urls, err := gitexec.LocalURLs(ctx, folder)
		if err != nil {
			return out, err
		}
		var changes []gitexec.URLChange
		for _, local := range urls {
			after, matched, err := repositorylayout.Translate(out.References, reference.Owner, local.Path)
			if err != nil {
				return out, err
			}
			if !matched {
				return out, fmt.Errorf("workspace %s borrows an unidentified local Git remote", folder)
			}
			if after != local.Path {
				changes = append(changes, gitexec.URLChange{Key: local.Key, Before: local.Value, After: after})
			}
		}
		if len(changes) > 0 {
			edit, err := gitexec.PlanConfigRelocation(ctx, folder, changes)
			if err != nil {
				return out, err
			}
			out.Git = append(out.Git, edit)
		}
	}
	return out, nil
}

// Purge removes the hub before completing external archival and deleting the
// project row. Deleted is irreversible intent, so that intentional absence must
// survive an interrupted purge without recreating an empty repository.
func missingRepositoryAllowed(owner project.RepositoryOwner, recorded bool) bool {
	return owner.Retained || owner.Lifecycle == project.Deleted || (!recorded && owner.DefaultBranch == "")
}
