package httpapi

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

type PackSetup struct {
	projects  *project.Projects
	lifecycle *projectlifecycle.Service
	ways      *wow.Service
}

func NewPackSetup(projects *project.Projects, lifecycle *projectlifecycle.Service, ways *wow.Service) PackSetup {
	return PackSetup{projects: projects, lifecycle: lifecycle, ways: ways}
}

// ApplyPack: POST /projects/{slug}/pack {name} — pinned guidance
// manifest + workflow installs in one shot (the wizard's engine).
func (h PackSetup) ApplyPack(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "name required"}, func(in PackChoice) {
		if in.Name == "" {
			WriteError(w, 400, "name required")
			return
		}

		pack, err := packNamed(r.Context(), h.ways, in.Name)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if pack == nil {
			WriteError(w, 404, "no such pack %q", in.Name)
			return
		}
		out, err := h.lifecycle.ApplyPack(r.Context(), p, pack, Actor(r))
		if err != nil {
			WriteFaultOr(w, err, 500)
			return
		}
		WriteJSON(w, 200, out)
	})
}

// SeedToolchain: POST /api/v1/projects/{slug}/toolchain {name} — write
// the stock toolchain declaration (stock/toolchains/<name>.toml) as the
// project's mise.toml and commit it. The repo declares WHAT; registries
// declare WHERE (design/subsystems/registries.md). No-op refusal if mise.toml exists —
// the project's declaration is its own.
func (h PackSetup) SeedToolchain(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if p.DefaultBranch == "" {
		WriteError(w, 422, "project is not repo-backed")
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "name required (e.g. java, go, node, python, rust)"}, func(in ToolchainChoice) {
		if in.Name == "" {
			WriteError(w, 400, "name required (e.g. java, go, node, python, rust)")
			return
		}

		wrote, err := h.lifecycle.SeedToolchain(r.Context(), p, in.Name, Actor(r))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				WriteError(w, 404, "no stock toolchain %q", in.Name)
				return
			}
			WriteFault(w, err)
			return
		}
		if !wrote {
			WriteError(w, 409, "mise.toml already exists — the project owns its declaration")
			return
		}
		WriteJSON(w, 200, ToolchainWritten{OK: true, Toolchain: in.Name})
	})
}

// ToolchainWritten names the toolchain declaration written for a project.
type ToolchainWritten struct {
	OK        bool   `json:"ok"`
	Toolchain string `json:"toolchain"`
}

// PackChoice is POST /projects/{slug}/pack: the pack to apply.
type PackChoice struct {
	Name string `json:"name"`
}

// ToolchainChoice is POST /projects/{slug}/toolchain: the stock toolchain
// to declare in mise.toml.
type ToolchainChoice struct {
	Name string `json:"name"`
}
