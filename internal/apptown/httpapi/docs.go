package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/stock/docs"
)

type DocIntegration struct{ Capability, Provider, Title string }
type Docs struct {
	integrations []DocIntegration
	links        DocLinks
}

// NewDocs serves the shipped docs. origin is this AppTown's public web origin
// ("https://forge.example"), or empty when it was not given one: the guides'
// live links then read as words, because a guessed address (the one a
// request happened to arrive on) is not one to hand a coding agent.
func NewDocs(integrations []DocIntegration, origin string) Docs {
	return Docs{integrations: slices.Clone(integrations), links: docLinks(origin)}
}

// gitPath is where AppTown serves its projects' repositories over HTTP:
// <origin>/git/<project>.
const gitPath = "/git"

// docLinks is this AppTown's own addresses, as the guides name them.
func docLinks(origin string) DocLinks {
	link := func(path string) DocLink {
		if origin == "" {
			return DocLink{Path: path}
		}
		return DocLink{URL: origin + path, Path: path}
	}
	return DocLinks{Origin: link(""), MCP: link(identity.AudienceUser), Git: link(gitPath)}
}

// handleListDocs: GET /api/v1/docs
func (h Docs) ListDocs(w http.ResponseWriter, r *http.Request) {
	guidesFS, err := fs.Sub(docs.FS, "guides")
	if err != nil {
		WriteError(w, 500, "guides: %v", err)
		return
	}
	groups, err := guideGroups(guidesFS)
	if err != nil {
		WriteError(w, 500, "%v", err)
		return
	}
	integs := []DocIntegrationEntry{}
	for _, c := range h.integrations {
		if _, err := docs.FS.ReadFile("integrations/" + c.Capability + "-" + c.Provider + ".admin.md"); err == nil {
			integs = append(integs, DocIntegrationEntry(c))
		}
	}
	WriteJSON(w, 200, DocsIndex{Groups: groups, Integrations: integs})
}

// guideIndexFile names the guides' groups and reading order (in the guides
// directory). The page opens on the first guide of the first group.
const guideIndexFile = "index.yaml"

// otherGuidesTitle heads the guides no group names, so a guide added without
// an index entry is still shown rather than hidden.
const otherGuidesTitle = "Other"

type guideIndex struct {
	Groups []struct {
		Title  string   `yaml:"title"`
		Guides []string `yaml:"guides"`
	} `yaml:"groups"`
}

// guideGroups reads the guides in fsys (the guides directory) into the
// index's groups, in its order. An index entry naming no guide is skipped, as
// is a group left empty by that; guides the index does not name follow, by
// file name, under "Other". An unreadable index is an error, not an empty or
// alphabetical page: the index ships in the binary and its tests read it.
func guideGroups(fsys fs.FS) ([]DocGuideGroup, error) {
	raw, err := fs.ReadFile(fsys, guideIndexFile)
	if err != nil {
		return nil, fmt.Errorf("guide index: %w", err)
	}
	// TestShippedGuideIndexNamesRealGuides decodes the shipped index
	// strictly, so a misspelt key fails there rather than here.
	var index guideIndex
	if err := yaml.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("guide index: %w", err)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("guides: %w", err)
	}
	onDisk := map[string]DocGuideEntry{}
	var slugs []string
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("guide %s: %w", slug, err)
		}
		onDisk[slug] = DocGuideEntry{Slug: slug, Title: guideTitle(slug, b)}
		slugs = append(slugs, slug)
	}
	groups := []DocGuideGroup{}
	placed := map[string]bool{}
	for _, g := range index.Groups {
		group := DocGuideGroup{Title: g.Title, Guides: []DocGuideEntry{}}
		for _, slug := range g.Guides {
			if entry, ok := onDisk[slug]; ok && !placed[slug] {
				group.Guides = append(group.Guides, entry)
				placed[slug] = true
			}
		}
		if len(group.Guides) > 0 {
			groups = append(groups, group)
		}
	}
	other := DocGuideGroup{Title: otherGuidesTitle, Guides: []DocGuideEntry{}}
	for _, slug := range slugs {
		if !placed[slug] {
			other.Guides = append(other.Guides, onDisk[slug])
		}
	}
	if len(other.Guides) > 0 {
		groups = append(groups, other)
	}
	return groups, nil
}

// guideTitle is a guide's first "# " heading, or its slug without one.
func guideTitle(slug string, content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		if t, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return slug
}

// handleGuideDoc: GET /api/v1/docs/guides/{slug}
func (h Docs) GuideDoc(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !project.ValidSlug(slug) {
		WriteError(w, 400, "bad slug")
		return
	}
	b, err := docs.FS.ReadFile("guides/" + slug + ".md")
	if err != nil {
		WriteError(w, 404, "no guide %s", slug)
		return
	}
	WriteJSON(w, 200, DocGuide{Slug: slug, Content: string(b), Links: h.links})
}

// DocGuideEntry is one guide in the docs index.
type DocGuideEntry struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// DocIntegrationEntry is one integration with documentation.
type DocIntegrationEntry struct {
	Capability string `json:"capability"`
	Provider   string `json:"provider"`
	Title      string `json:"title"`
}

// DocGuideGroup is one heading on the Docs page and its guides, in reading
// order.
type DocGuideGroup struct {
	Title  string          `json:"title"`
	Guides []DocGuideEntry `json:"guides"`
}

// DocsIndex lists the guides, grouped in reading order, and the documented
// integrations.
type DocsIndex struct {
	Groups       []DocGuideGroup       `json:"groups"`
	Integrations []DocIntegrationEntry `json:"integrations"`
}

// DocGuide is one guide's markdown and the addresses its live links name.
type DocGuide struct {
	Slug    string   `json:"slug"`
	Content string   `json:"content"`
	Links   DocLinks `json:"links"`
}

// DocLinks is the whole vocabulary of live links: a guide writes
// {apptown:<name>}, where <name> is a field's JSON name, and the Docs page
// shows this AppTown's own address there (web/src/liveLinks.ts). Only
// addresses that are the same for everyone belong here, never a person's
// token or anything secret: the docs are readable without signing in.
type DocLinks struct {
	Origin DocLink `json:"origin"` // the web address people and the CLI's APPTOWN_URL use
	MCP    DocLink `json:"mcp"`    // where a coding agent connects
	Git    DocLink `json:"git"`    // the base of a project's repository address
}

// DocLink is one live link. URL is empty when AppTown does not know its
// public address; the page then says "your AppTown's address" followed by
// Path instead of linking anywhere.
type DocLink struct {
	URL  string `json:"url"`
	Path string `json:"path"`
}
