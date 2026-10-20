package projectlifecycle

import (
	execcontext "context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"flokkit.io/forge/internal/forge/scm"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/stock"
)

// ApplyPack adopts a pack on a project: pinned guidance, the platform tier,
// lane selection, policies, seed files, toolchain, extensions. The domain
// command behind the HTTP door and the MCP's create_project.
func (s *Service) ApplyPack(ctx execcontext.Context, p *project.Project, pack *wow.Pack, who string) (*PackApplied, error) {
	// Applying a published pack uses the same explicit adoption transaction as
	// the Ways of working page. No boot-time placement or latest-block guesses.
	// The pack's contents were read from the publication it offers
	// (pack.Version); the same publication is the one adopted, so what is
	// installed, seeded and reported is one version.
	if pack.Version <= 0 {
		return nil, fmt.Errorf("pack %s has no published version to apply", pack.Name)
	}
	picks := []adoption.PackPick{{Pack: pack.Name, Version: pack.Version}}
	if p.DefaultBranch != "" && pack.Name != "forge" {
		platform, err := s.ways.Pack(ctx, "forge")
		if err != nil {
			return nil, fmt.Errorf("selecting published pack forge: %w", err)
		}
		picks = append(picks, adoption.PackPick{Pack: "forge", Version: platform.Version})
	}
	c, err := s.adoption.PreparePack(ctx, p.ID, picks)
	if err != nil {
		return nil, err
	}
	// Availability comes from the instance declaration; the sealed selection is unchanged.
	for _, ext := range pack.Extensions {
		if !slices.Contains(s.extensions, ext) {
			return nil, fmt.Errorf("unknown extension %q", ext)
		}
	}
	policiesInstalled := []string{}
	for _, policy := range pack.Policies {
		policiesInstalled = append(policiesInstalled, policy.Name)
	}
	if _, err := s.Adopt(ctx, p, c, who, "pack "+pack.Name+" applied", adoption.OperationApplyPack); err != nil {
		return nil, err
	}
	pos := len(c.Receipt.Guidance)
	installed := append([]string{}, pack.Workflows...)
	var missing []string
	// Seed files: stock/seeds/<pack>/ copied into the repo when absent —
	// a starter to change, not a blank page (design/subsystems/html-games.md).
	// Files reach an established project only through a change, so there
	// the adoption stands and the files are named as not written.
	var skipped []string
	seeded, refused, err := s.seedPackFiles(ctx, p, pack.StarterPack(), who)
	for _, r := range refused {
		missing = append(missing, "starter file "+r)
	}
	if errors.Is(err, ErrProjectEstablished) {
		skipped = append(skipped, "starter files")
	} else if err != nil {
		missing = append(missing, "starter files: "+err.Error())
	}
	// The pack's toolchain declaration (mise.toml) ships with it: guidance
	// that says "declared in mise.toml" is a lie until the file exists.
	toolchainSeeded := ""
	if pack.Toolchain != "" && p.DefaultBranch != "" {
		if wrote, err := s.SeedToolchain(ctx, p, pack.Toolchain, who); errors.Is(err, ErrProjectEstablished) {
			skipped = append(skipped, "mise.toml")
		} else if err != nil {
			missing = append(missing, "toolchain:"+pack.Toolchain)
		} else if wrote {
			toolchainSeeded = pack.Toolchain
		}
	}
	violations, err := s.adoption.ProjectConformance(ctx, s.db, p.ID)
	if err != nil {
		return nil, err
	}
	if violations == nil {
		violations = []adoption.Violation{}
	}
	var hash string
	_, hash, _, _ = s.adoption.Compile(ctx, p.ID)
	audit.At(s.now()).BestEffort(ctx, s.db, who, p.ID, "pack.applied", pack.Name,
		map[string]any{"guidance": pos, "workflows": installed, "policies": policiesInstalled,
			"missing": missing, "guidanceHash": hash, "policyWarnings": len(violations), "seeded": seeded,
			"toolchain": toolchainSeeded, "skipped": skipped})
	return &PackApplied{Pack: pack.Name, GuidanceImports: pos,
		WorkflowsInstalled: installed, PoliciesInstalled: policiesInstalled,
		Missing: missing, GuidanceHash: hash, PolicyWarnings: violations, Seeded: seeded,
		Toolchain: toolchainSeeded, Skipped: skipped}, nil
}

// PackApplied is what applying a pack did: the ways of working it adopted,
// the starter files and toolchain it wrote, and what it could not.
type PackApplied struct {
	Pack               string               `json:"pack"`
	GuidanceImports    int                  `json:"guidanceImports"`
	WorkflowsInstalled []string             `json:"workflowsInstalled"`
	PoliciesInstalled  []string             `json:"policiesInstalled"`
	Missing            []string             `json:"missing"`
	GuidanceHash       string               `json:"guidanceHash"`
	PolicyWarnings     []adoption.Violation `json:"policyWarnings"`
	Seeded             []string             `json:"seeded"`
	Toolchain          string               `json:"toolchain"`
	Skipped            []string             `json:"skipped"`
}

// seedPackFiles copies stock/seeds/<pack>/ into a new project's repo,
// skipping any file that already exists (an existing repo is never
// overwritten), and commits the batch. Returns the paths written, and each
// file refused because the forge would have written through a link the
// repository holds, with why: that file is skipped, the rest are written.
func (s *Service) seedPackFiles(ctx execcontext.Context, p *project.Project, packName, who string) (written, refused []string, err error) {
	if p.DefaultBranch == "" {
		return nil, nil, nil
	}
	written, err = s.CommitGenesis(ctx, p, fmt.Sprintf("apptown: seed %s starter (pack apply)", packName), who,
		func(dir string) ([]string, error) {
			var w []string
			w, refused, err = writePackSeeds(dir, packName)
			return w, err
		})
	return written, refused, err
}

// StarterIsStatic says whether a pack's starter app, as its seed's
// .forge/app.yaml declares it, is static files only: no part runs a server
// and nothing is needed, so a database or cache would have nothing to serve.
// A pack whose seed declares no contract (or no seed) says nothing: false.
func StarterIsStatic(packName string) bool {
	raw, err := stock.FS.ReadFile("seeds/" + packName + "/.forge/app.yaml")
	if err != nil {
		return false
	}
	c, err := appcontract.Parse(raw)
	if err != nil || len(c.Needs) > 0 || len(c.Init) > 0 || len(c.Parts) == 0 {
		return false
	}
	for _, part := range c.Parts {
		if !part.IsStatic() {
			return false
		}
	}
	return true
}

func writePackSeeds(dir, packName string) (written, refused []string, _ error) {
	root := "seeds/" + packName
	err := fs.WalkDir(stock.FS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a pack without seeds is the common case
		}
		rel := strings.TrimPrefix(path, root+"/")
		body, err := stock.FS.ReadFile(path)
		if err != nil {
			return nil
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(rel, "bin/") {
			mode = 0o755 // binstubs (bin/rails) must run; the embed FS keeps no modes
		}
		// Never clobber, and never follow a link the repository holds
		// (scm.CreateInTree) — except that ignore rules MERGE: a repo init
		// writes a .gitignore before the seed arrives, and a way of working's
		// rules (.godot/, build/, node_modules/…) must not be dropped for it
		// (Duck Storm shipped without ".godot/" and failed its own check).
		// A path through a link the repository holds is refused for that
		// file alone: the rest of the starter is
		// written, and the refusal is named.
		var unsafe *scm.UnsafePath
		created, err := scm.CreateInTree(dir, rel, body, mode)
		if errors.As(err, &unsafe) {
			refused = append(refused, unsafe.Path+" not written: "+unsafe.Why)
			return nil
		}
		if err != nil {
			return err
		}
		if created {
			written = append(written, rel)
			return nil
		}
		if filepath.Base(rel) == ".gitignore" {
			merged, err := scm.UpdateInTree(dir, rel, func(existing []byte) []byte { return mergeIgnoreLines(existing, body) })
			if errors.As(err, &unsafe) {
				refused = append(refused, unsafe.Path+" not written: "+unsafe.Why)
				return nil
			}
			if err != nil {
				return err
			}
			if merged {
				written = append(written, rel)
			}
		}
		return nil
	})
	return written, refused, err
}

// SeedToolchain writes stock/toolchains/<name>.toml as a new project's
// mise.toml, commits, and pushes. Returns (false, nil) when mise.toml
// already exists — the project's declaration is its own, never clobbered.
// An established project is refused with ErrProjectEstablished.
func (s *Service) SeedToolchain(ctx execcontext.Context, p *project.Project, name, who string) (bool, error) {
	body, err := stock.FS.ReadFile("toolchains/" + name + ".toml")
	if err != nil {
		return false, fmt.Errorf("no stock toolchain %q: %w", name, fs.ErrNotExist)
	}
	written, err := s.CommitGenesis(ctx, p, "apptown: declare "+name+" toolchain", who,
		func(dir string) ([]string, error) {
			created, err := scm.CreateInTree(dir, "mise.toml", body, 0o644)
			if err != nil || !created {
				return nil, err
			}
			return []string{"mise.toml"}, nil
		})
	if err != nil || len(written) == 0 {
		return false, err
	}
	audit.At(s.now()).BestEffort(ctx, s.db, who, p.ID, "project.toolchain.seeded", p.Slug, map[string]any{"toolchain": name})
	return true, nil
}

// mergeIgnoreLines appends the seed's ignore lines that the existing file
// lacks (comments and blanks from the seed travel with their rules), or
// returns existing unchanged.
func mergeIgnoreLines(existing, seed []byte) []byte {
	have := map[string]bool{}
	for _, ln := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	var add []string
	var pendingComments []string
	for _, ln := range strings.Split(strings.TrimRight(string(seed), "\n"), "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			pendingComments = nil
		case strings.HasPrefix(t, "#"):
			pendingComments = append(pendingComments, ln)
		case !have[t]:
			add = append(add, pendingComments...)
			add = append(add, ln)
			have[t] = true
			pendingComments = nil
		default:
			pendingComments = nil
		}
	}
	if len(add) == 0 {
		return existing
	}
	return []byte(strings.TrimRight(string(existing), "\n") + "\n\n" + strings.Join(add, "\n") + "\n")
}
