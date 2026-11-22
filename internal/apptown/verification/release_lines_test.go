package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/internal/packagefacts"
)

// cycleFacts is a fake endoflife.date: the named products' cycles, every
// other package untracked, or err for every read. Registry lines come from
// lines by package name.
type cycleFacts struct {
	products map[string]packagefacts.Support
	lines    map[string]packagefacts.Lines
	err      error
}

func (f cycleFacts) Support(_ context.Context, p packagefacts.Package) (packagefacts.Support, error) {
	if f.err != nil {
		return packagefacts.Support{}, f.err
	}
	return f.products[p.Name], nil
}
func (f cycleFacts) Lines(_ context.Context, p packagefacts.Package, _ string, _ func(string) string) (packagefacts.Lines, error) {
	if l, ok := f.lines[p.Name]; ok {
		return l, nil
	}
	return packagefacts.Lines{}, errors.New("no registry in this fake")
}

// semverLine is packagefacts.Line for any package: the line of a package
// endoflife.date does not track.
func semverLine(_ packagefacts.Package, v string) string { return packagefacts.Line(v) }

func cycle(name, released, ends string) packagefacts.Cycle {
	c := packagefacts.Cycle{Name: name, Released: majorDay(released), Ends: majorDay(ends)}
	return c
}

// knownCycles: the runtimes these tests move, as endoflife.date lists them
// on 2026-11-22 (node 24's end moved into the past so a 24 → 26 move is due).
var knownCycles = cycleFacts{products: map[string]packagefacts.Support{
	"library/node":   {Tracked: true, Product: "nodejs", Cycles: []packagefacts.Cycle{cycle("26", "2026-05-05", "2029-04-30"), {Name: "24", Released: majorDay("2024-05-06"), Ends: majorDay("2026-04-30"), Ended: true}}},
	"library/caddy":  {Tracked: true, Product: "caddy", Cycles: []packagefacts.Cycle{{Name: "2", Released: majorDay("2020-05-04")}}},
	"library/golang": {Tracked: true, Product: "go", Cycles: []packagefacts.Cycle{{Name: "1.27", Released: majorDay("2026-08-19")}, cycle("1.26", "2026-02-10", "2027-08-01")}},
	"library/python": {Tracked: true, Product: "python", Cycles: []packagefacts.Cycle{cycle("3.15", "2026-10-09", "2031-10-31"), cycle("3.14", "2025-10-07", "2030-10-31"), cycle("3.13", "2024-10-07", "2029-10-31"), cycle("3.12", "2023-10-02", "2028-10-31")}},
	"library/php":    {Tracked: true, Product: "php", Cycles: []packagefacts.Cycle{cycle("8.5", "2025-11-20", "2029-12-31"), cycle("8.4", "2024-11-21", "2028-12-31"), cycle("8.3", "2023-11-23", "2027-12-31")}},
}}

// imageMove compares one Dockerfile FROM move with the given facts and
// level, returning the error and the recorded line decisions.
func imageMove(t *testing.T, facts MajorFacts, level, old, next string, now time.Time) ([]MajorMove, imageline.Scope, error) {
	t.Helper()
	tree := t.TempDir()
	write := func(value string) map[string]any {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		inputs, err := UpdateInputs(tree)
		if err != nil {
			t.Fatal(err)
		}
		return inputs
	}
	digest := "sha256:" + strings.Repeat("e", 64)
	before := write("FROM " + old + "@sha256:" + strings.Repeat("f", 64) + " AS runtime\n")
	after := write("FROM " + next + "@" + digest + " AS runtime\n")
	r := &updateResolver{answer: imagelines.Reading{Answer: imageline.Answer{Tag: next, Digest: digest}}}
	var lines []MajorMove
	err := CompareUpdateInputs(t.Context(), before, after, true, Majors{Facts: facts, Now: func() time.Time { return now }}.Images(ImageUpdates{Resolver: r, Scope: UpdateScope(level), Lines: &lines}))
	return lines, r.scope, err
}

// Fieldnotes (22 Nov): a Routine update moved php:8.3.35-apache-bookworm to
// 8.5.12. A language or runtime endoflife.date tracks moves by its release
// cycle: Routine stays on 8.3 (and Python on 3.12); another cycle is not a
// Routine move, whatever semver says.
func TestRoutineKeepsAnImageOnItsReleaseLine(t *testing.T) {
	t.Parallel()
	now := majorDay("2026-11-22")
	for _, tc := range []struct{ old, next, line string }{
		{"php:8.3.35-apache-bookworm", "php:8.5.12-apache-bookworm", "8.3"},
		{"php:8.3-apache-bookworm", "php:8.4.1-apache-bookworm", "8.3"},
		{"python:3.12.8-slim", "python:3.13.1-slim", "3.12"},
	} {
		lines, _, err := imageMove(t, knownCycles, "routine", tc.old, tc.next, now)
		if err == nil || !strings.Contains(err.Error(), "leaves its line "+tc.line) || !strings.Contains(err.Error(), "Routine keeps it on "+tc.line) {
			t.Fatalf("%s → %s: %v", tc.old, tc.next, err)
		}
		if len(lines) != 1 || lines[0].Allowed || lines[0].FromLine != tc.line || lines[0].LineUnknown != "" {
			t.Fatalf("%s → %s recorded %+v", tc.old, tc.next, lines)
		}
	}
	for _, tc := range []struct{ old, next, line string }{
		{"php:8.3.35-apache-bookworm", "php:8.3.36-apache-bookworm", "8.3"},
		{"python:3.12.8-slim", "python:3.12.9-slim", "3.12"},
		{"node:24.1-slim", "node:24.9.0-slim", "24"},
	} {
		lines, scope, err := imageMove(t, knownCycles, "routine", tc.old, tc.next, now)
		if err != nil || len(lines) != 1 || !lines[0].Allowed || lines[0].ToLine != tc.line || scope != imageline.Line {
			t.Fatalf("%s → %s: %v %+v %s", tc.old, tc.next, err, lines, scope)
		}
	}
}

// At Including major an image leaves its release line only for the newest
// cycle and only when a rule holds, and the decision names it.
// endoflife.date gives no release count, so the settled rule never moves an
// image: no count is invented.
func TestIncludingMajorMovesAnImageLineOnlyWhenDue(t *testing.T) {
	t.Parallel()
	ending := cycleFacts{products: map[string]packagefacts.Support{"library/python": {Tracked: true, Product: "python", Cycles: []packagefacts.Cycle{cycle("3.15", "2026-10-09", "2031-10-31"), cycle("3.12", "2023-10-02", "2027-01-31")}}}}
	settledNewest := cycleFacts{products: map[string]packagefacts.Support{"library/python": {Tracked: true, Product: "python", Cycles: []packagefacts.Cycle{cycle("3.14", "2025-10-07", "2030-10-31"), cycle("3.12", "2023-10-02", "2028-10-31")}}}}
	now := majorDay("2026-11-22")
	for _, tc := range []struct {
		name  string
		facts cycleFacts
		next  string
		pass  bool
		why   []string
	}{
		{"current line ends within three months", ending, "python:3.15.1-slim", true, []string{"python 3.12 → 3.15 due", "3.12 ends support 2027-01-31 (endoflife.date)"}},
		{"supported current line, young newest", knownCycles, "python:3.15.1-slim", false, []string{"3.12 is supported until 2028-10-31", "Keep library/python on 3.12"}},
		{"not the newest line", knownCycles, "python:3.13.1-slim", false, []string{"a major move goes to the newest line, 3.15"}},
		{"settled newest without a release count", settledNewest, "python:3.14.2-slim", false, []string{"3.12 is supported until 2028-10-31", "how many releases followed 3.14 (released 2025-10-07): endoflife.date does not say"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, _, err := imageMove(t, tc.facts, "aggressive", "python:3.12.8-slim", tc.next, now)
			if (err == nil) != tc.pass || len(lines) != 1 || lines[0].Allowed != tc.pass {
				t.Fatalf("pass=%t: %v %+v", tc.pass, err, lines)
			}
			text := lines[0].Text()
			for _, w := range tc.why {
				if !strings.Contains(text, w) {
					t.Errorf("lacks %q: %s", w, text)
				}
			}
			if strings.Contains(text, "releases since") {
				t.Errorf("a release count was invented: %s", text)
			}
		})
	}
}

// With endoflife.date unreachable (or not tracking the image), the line is
// x.y: a patch still moves, another x.y does not at any level, and the
// evidence says the line is unknown.
func TestAnUnknownReleaseLineIsTheNarrowest(t *testing.T) {
	t.Parallel()
	now := majorDay("2026-11-22")
	down := cycleFacts{err: errors.New("dial tcp: lookup endoflife.date: no such host")}
	untracked := cycleFacts{}
	for _, facts := range []cycleFacts{down, untracked} {
		lines, _, err := imageMove(t, facts, "routine", "php:8.3.35-apache-bookworm", "php:8.3.36-apache-bookworm", now)
		if err != nil || len(lines) != 1 || !lines[0].Allowed || !strings.Contains(lines[0].LineUnknown, "the release line of library/php is unknown") {
			t.Fatalf("patch: %v %+v", err, lines)
		}
		for _, level := range []string{"routine", "aggressive"} {
			lines, _, err := imageMove(t, facts, level, "php:8.3.35-apache-bookworm", "php:8.4.1-apache-bookworm", now)
			if err == nil || len(lines) != 1 || lines[0].Allowed || !strings.Contains(err.Error(), "Unknown:") {
				t.Fatalf("%s 8.3 → 8.4: %v %+v", level, err, lines)
			}
		}
	}
}

// Direct dependencies: a product endoflife.date tracks stays on its cycle at
// Routine (Django 5.1 → 5.2 leaves it); an untracked package keeps semver
// lines (a minor moves, a major does not); when endoflife.date cannot be
// read every package is held to x.y.
func TestRoutineKeepsDirectDependenciesOnTheirReleaseLines(t *testing.T) {
	t.Parallel()
	dep := func(typ, name, version string) packagefacts.Dependency {
		return packagefacts.Dependency{Package: packagefacts.Package{Type: typ, Name: name}, Version: version, Manifest: "."}
	}
	django := packagefacts.Support{Tracked: true, Product: "django", Cycles: []packagefacts.Cycle{{Name: "5.2"}, {Name: "5.1"}}}
	facts := cycleFacts{products: map[string]packagefacts.Support{"django": django}}
	base := []packagefacts.Dependency{dep(packagefacts.PyPI, "django", "5.1.4"), dep(packagefacts.NPM, "lodash", "4.17.21"), dep(packagefacts.NPM, "left-pad", "1.3.0")}
	for _, tc := range []struct {
		name      string
		facts     cycleFacts
		candidate []packagefacts.Dependency
		refused   int
		why       []string
	}{
		{"tracked minor is another line", facts, []packagefacts.Dependency{dep(packagefacts.PyPI, "django", "5.2.1"), base[1], base[2]}, 1, []string{"django 5.1 → 5.2 not allowed at Routine: 5.2.1 leaves its line 5.1 (endoflife.date django)"}},
		{"tracked patch stays", facts, []packagefacts.Dependency{dep(packagefacts.PyPI, "django", "5.1.5"), base[1], base[2]}, 0, nil},
		{"untracked minor stays on its major", facts, []packagefacts.Dependency{base[0], dep(packagefacts.NPM, "lodash", "4.18.0"), base[2]}, 0, nil},
		{"untracked major leaves its line", facts, []packagefacts.Dependency{base[0], base[1], dep(packagefacts.NPM, "left-pad", "2.0.0")}, 1, []string{"left-pad 1 → 2 not allowed at Routine"}},
		{"endoflife.date down holds a minor to x.y", cycleFacts{err: errors.New("offline")}, []packagefacts.Dependency{base[0], dep(packagefacts.NPM, "lodash", "4.18.0"), base[2]}, 1, []string{"lodash 4.17 → 4.18 not allowed at Routine", "the release line of lodash is unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Majors{Facts: tc.facts}.Check(t.Context(), [16]byte{}, base, tc.candidate, false)
			if err != nil || len(got) != tc.refused {
				t.Fatalf("%v %+v", err, got)
			}
			var text []string
			for _, m := range got {
				if m.Allowed {
					t.Errorf("allowed at Routine: %s", m.Text())
				}
				text = append(text, m.Text())
			}
			for _, w := range tc.why {
				if !strings.Contains(strings.Join(text, "\n"), w) {
					t.Errorf("lacks %q: %s", w, text)
				}
			}
		})
	}
}
