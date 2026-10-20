package pi

import "flokkit.io/forge/internal/dockerlabel"

// Opt-in pi extensions. AppTown curates extension CODE to a small blessed set
// BAKED into the agent image (pihome.go: trust is "never", so a repo can never
// load its own extension). pi-subagents is always on. The rest ship baked but
// OFF, and a project turns them on — by a pack that lists them or directly on
// the project. Enabling only adds the package to that session's settings.json
// packages; the code is already baked and pinned by the image.
//
// Adding a new blessed extension = bake it in deploy/apptown-agent/Dockerfile
// (with an AppTown.<name>.version label) and add a row here.

type Extension struct {
	Name    string `json:"name"`    // short id used in packs and the API ("lsp")
	Package string `json:"package"` // the npm package baked into /opt/pi-baked
	Label   string `json:"-"`       // image label carrying the baked, pinned version
	Desc    string `json:"description"`
}

// optionalExtensions are baked-but-off; a project opts in. (pi-subagents is
// handled separately in pihome.go — it is always on when the image bakes it.)
var optionalExtensions = []Extension{
	{
		Name:    "lsp",
		Package: "@narumitw/pi-lsp",
		Label:   dockerlabel.PiLSPVersion,
		Desc:    "Real-time LSP diagnostics — lsp_diagnostics / lsp_fix — for fast intermediate feedback while editing (biome/ruff/gopls on PATH). The authoritative checks still gate.",
	},
}

func optionalExtension(name string) *Extension {
	for i := range optionalExtensions {
		if optionalExtensions[i].Name == name {
			return &optionalExtensions[i]
		}
	}
	return nil
}

// Extensions returns the curated, baked-but-optional extension catalog.
func Extensions() []Extension { return append([]Extension(nil), optionalExtensions...) }
