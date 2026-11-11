package httpapi

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	stockdocs "github.com/flokkit/apptown/stock/docs"
	"gopkg.in/yaml.v3"
)

// guidesBeingWritten are slugs the shipped index names before their guide
// exists, each with the reason. The page skips them until the file lands;
// any other index slug without a guide fails TestShippedGuideIndexNamesRealGuides,
// so the index cannot silently rot.
var guidesBeingWritten = map[string]string{}

func guide(title string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("# " + title + "\n\nbody\n")}
}

type groupView struct {
	Title string
	Slugs []string
}

func view(groups []DocGuideGroup) []groupView {
	out := []groupView{}
	for _, g := range groups {
		v := groupView{Title: g.Title}
		for _, e := range g.Guides {
			v.Slugs = append(v.Slugs, e.Slug)
		}
		out = append(out, v)
	}
	return out
}

// The Docs page shows guides in the index's groups and order, never
// alphabetically; a guide the index does not name still appears, under
// Other; an index slug with no guide is skipped, and a group left empty by
// that is not shown.
func TestGuideGroupsFollowTheIndexAndHideNothing(t *testing.T) {
	fsys := fstest.MapFS{
		"index.yaml": {Data: []byte(`groups:
  - title: Start here
    guides: [quickstart]
  - title: Building
    guides: [zeta, alpha, not-written-yet]
  - title: Running
    guides: [also-not-written]
`)},
		"quickstart.md": guide("Quickstart"),
		"alpha.md":      guide("Alpha guide"),
		"zeta.md":       guide("Zeta guide"),
		"stray.md":      {Data: []byte("no heading\n")},
		"another.md":    guide("Another"),
		"notes.txt":     {Data: []byte("not a guide")},
	}
	groups, err := guideGroups(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []groupView{
		{"Start here", []string{"quickstart"}},
		{"Building", []string{"zeta", "alpha"}},
		{"Other", []string{"another", "stray"}},
	}
	if got := view(groups); !slices.EqualFunc(got, want, func(a, b groupView) bool { return a.Title == b.Title && slices.Equal(a.Slugs, b.Slugs) }) {
		t.Fatalf("groups = %+v, want %+v", got, want)
	}
	if groups[1].Guides[0].Title != "Zeta guide" || groups[2].Guides[1].Title != "stray" {
		t.Fatalf("titles come from the first heading, else the slug: %+v", groups)
	}
}

// With every guide named there is no Other group.
func TestGuideGroupsHaveNoOtherWhenAllAreIndexed(t *testing.T) {
	groups, err := guideGroups(fstest.MapFS{
		"index.yaml":    {Data: []byte("groups:\n  - title: Start here\n    guides: [quickstart]\n")},
		"quickstart.md": guide("Quickstart"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Title != "Start here" {
		t.Fatalf("groups = %+v", view(groups))
	}
}

// A missing or malformed index is an error, not an alphabetical page that
// looks fine.
func TestGuideGroupsRefuseABrokenIndex(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"missing":   {"quickstart.md": guide("Quickstart")},
		"malformed": {"index.yaml": {Data: []byte("groups: [title: A\n")}, "quickstart.md": guide("Quickstart")},
	} {
		if _, err := guideGroups(fsys); err == nil {
			t.Errorf("%s index: no error", name)
		}
	}
}

// The shipped index uses only known keys (a misspelt "guide:" would drop a
// group's guides into Other), and every slug it names is a shipped guide,
// except a guide being written; once that guide lands it leaves the
// exception list.
func TestShippedGuideIndexNamesRealGuides(t *testing.T) {
	raw, err := stockdocs.FS.ReadFile("guides/" + guideIndexFile)
	if err != nil {
		t.Fatal(err)
	}
	var index guideIndex
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&index); err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, g := range index.Groups {
		for _, slug := range g.Guides {
			if named[slug] {
				t.Errorf("index names %s twice", slug)
			}
			named[slug] = true
			_, err := fs.Stat(stockdocs.FS, "guides/"+slug+".md")
			_, pending := guidesBeingWritten[slug]
			switch {
			case err != nil && !pending:
				t.Errorf("index names %s, which is not a shipped guide", slug)
			case err == nil && pending:
				t.Errorf("%s now exists: remove it from guidesBeingWritten", slug)
			}
		}
	}
	for slug := range guidesBeingWritten {
		if !named[slug] {
			t.Errorf("%s is listed as being written but the index does not name it", slug)
		}
	}
}

// The served page opens on Start here's quickstart and lists every shipped
// guide exactly once.
func TestDocsListOpensOnStartHere(t *testing.T) {
	w := httptest.NewRecorder()
	NewDocs(nil, "").ListDocs(w, httptest.NewRequest("GET", "/api/v1/docs", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got DocsIndex
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) == 0 || got.Groups[0].Title != "Start here" || got.Groups[0].Guides[0].Slug != "quickstart" {
		t.Fatalf("first group: %+v", got.Groups)
	}
	entries, err := stockdocs.FS.ReadDir("guides")
	if err != nil {
		t.Fatal(err)
	}
	shown := map[string]int{}
	for _, g := range got.Groups {
		for _, e := range g.Guides {
			shown[e.Slug]++
		}
	}
	for _, e := range entries {
		if slug, ok := strings.CutSuffix(e.Name(), ".md"); ok && shown[slug] != 1 {
			t.Errorf("guide %s shown %d times", slug, shown[slug])
		}
	}
}

// A guide comes with this AppTown's own addresses for its live links when the
// AppTown knows its public origin, and with only their paths when it does not,
// so the page says "your AppTown's address" rather than inventing one.
func TestGuideDocCarriesAppTownsOwnAddresses(t *testing.T) {
	read := func(origin string) DocLinks {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/docs/guides/quickstart", nil)
		r.SetPathValue("slug", "quickstart")
		w := httptest.NewRecorder()
		NewDocs(nil, origin).GuideDoc(w, r)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var got DocGuide
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Content == "" {
			t.Fatal("guide served without its content")
		}
		return got.Links
	}
	known := DocLinks{
		Origin: DocLink{URL: "https://forge.example", Path: ""},
		MCP:    DocLink{URL: "https://forge.example/mcp/user", Path: "/mcp/user"},
		Git:    DocLink{URL: "https://forge.example/git", Path: "/git"},
	}
	if got := read("https://forge.example"); got != known {
		t.Errorf("with an origin: %+v, want %+v", got, known)
	}
	unknown := DocLinks{Origin: DocLink{}, MCP: DocLink{Path: "/mcp/user"}, Git: DocLink{Path: "/git"}}
	if got := read(""); got != unknown {
		t.Errorf("without an origin: %+v, want %+v", got, unknown)
	}
}

// The guides people read on a phone are short sentences, steps and lists:
// no paragraph or list item runs past 80 words, and the internal words that
// made them hard to read (SUPPRESS, posture, exit contract, VEX-shaped) are
// gone. Fenced code, HTML comments and table rows are not prose.
func TestPhoneReadGuidesAreShortAndPlain(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"guides/quickstart.md", "guides/setups.md", "integrations/scanner-tool.developer.md", "integrations/scanner-tool.admin.md"} {
		raw, err := fs.ReadFile(stockdocs.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, old := range []string{"SUPPRESS", "posture", "exit contract", "VEX-shaped"} {
			if strings.Contains(text, old) {
				t.Errorf("%s still says %q", name, old)
			}
		}
		for _, para := range guideProse(text) {
			if n := len(strings.Fields(para)); n > 80 {
				t.Errorf("%s has a %d-word paragraph: %.60q", name, n, para)
			}
		}
	}
}

// The developer guide's account of what a landing runs keeps the manual
// scanner limit: attaching a manual scanner does not make it run, so a reader
// is not told a landing checked what it did not.
func TestScannerGuideSaysManualScannersRunOnlyFromAToolCheck(t *testing.T) {
	t.Parallel()
	raw, err := fs.ReadFile(stockdocs.FS, "integrations/scanner-tool.developer.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"runs each attached source and image scanner that is not manual",
		"A manual scanner runs only when a way of working's tool check names it.",
		"Attaching one to a project does not make it run at a landing.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the developer scanner guide does not say %q", want)
		}
	}
	if strings.Contains(text, "runs every attached scanner") {
		t.Error("the developer scanner guide says every attached scanner runs at a landing")
	}
}

// guideProse splits a guide into its paragraphs and list items, leaving out
// fenced code, HTML comments and table rows.
func guideProse(text string) []string {
	var out, cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	fenced, comment := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flush()
			fenced = !fenced
			continue
		case fenced:
			continue
		case strings.HasPrefix(trimmed, "<!--"):
			flush()
			comment = !strings.Contains(trimmed, "-->")
			continue
		case comment:
			comment = !strings.Contains(trimmed, "-->")
			continue
		case trimmed == "", strings.HasPrefix(trimmed, "|"), strings.HasPrefix(trimmed, "#"):
			flush()
			continue
		}
		if listItem(trimmed) {
			flush()
		}
		cur = append(cur, trimmed)
	}
	flush()
	return out
}

func listItem(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		return true
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && strings.HasPrefix(line[i:], ". ")
}
