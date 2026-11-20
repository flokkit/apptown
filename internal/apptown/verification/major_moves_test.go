package verification

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/packagefacts"
)

func majorDay(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// The Including major level stays on a supported major and moves to the
// newest major only when one of the owner's three rules holds; an unread fact
// never allows a move.
func TestIncludingMajorMovesOnlyWhenAMajorIsDue(t *testing.T) {
	t.Parallel()
	now := majorDay("2026-11-21")
	django := packagefacts.Dependency{Package: packagefacts.Package{Type: packagefacts.PyPI, Name: "django"}, Version: "4.2.16", Manifest: "pyproject.toml"}
	young := packagefacts.Lines{Newest: "5", First: packagefacts.Release{Version: "5.0", Published: majorDay("2026-09-20")}, Since: 3}
	settled := packagefacts.Lines{Newest: "5", First: packagefacts.Release{Version: "5.0", Published: majorDay("2026-05-01")}, Since: 2}
	supported := packagefacts.Support{Tracked: true, Product: "django", Cycles: []packagefacts.Cycle{{Name: "4.2", Ends: majorDay("2028-04-30")}, {Name: "4.1", Ends: majorDay("2023-12-01"), Ended: true}}}
	ending := packagefacts.Support{Tracked: true, Product: "django", Cycles: []packagefacts.Cycle{{Name: "4.2", Ends: majorDay("2027-01-31")}, {Name: "4.1", Ends: majorDay("2023-12-01"), Ended: true}}}
	ended := packagefacts.Support{Tracked: true, Product: "django", Cycles: []packagefacts.Cycle{{Name: "4.2", Ends: majorDay("2026-04-30"), Ended: true}}}
	fixedOnFive := []check.Finding{{Identifiers: []string{"CVE-2026-1"}, Package: &check.PackageRef{Name: "Django", Version: "4.2.16", Ecosystem: "PyPI", FixedIn: "5.0.3"}}}
	fixedOnFour := []check.Finding{{Identifiers: []string{"CVE-2026-2"}, Package: &check.PackageRef{Name: "django", Version: "4.2.16", Ecosystem: "PyPI", FixedIn: "4.2.17"}}}
	offline := errors.New("dial tcp: connection refused")
	for _, c := range []struct {
		name       string
		toLine     string
		lines      packagefacts.Lines
		linesErr   error
		support    packagefacts.Support
		supportErr error
		findings   []check.Finding
		allowed    bool
		why        []string
		unknown    []string
	}{
		{name: "a young major with a supported current one is not due", toLine: "5", lines: young, support: supported, findings: fixedOnFour,
			why: []string{"5.0 was released 2026-09-20 with 3 releases since", "4.2 is supported until 2028-04-30", "Keep django on 4"}},
		{name: "a major whose support has ended is due", toLine: "5", lines: young, support: ended, allowed: true,
			why: []string{"4.2 ended support 2026-04-30 (endoflife.date)"}},
		{name: "a major whose support ends within three months is due", toLine: "5", lines: young, support: ending, allowed: true,
			why: []string{"4.2 ends support 2027-01-31 (endoflife.date)"}},
		{name: "a security fix only on the newer major is due", toLine: "5", lines: young, support: supported, findings: fixedOnFive, allowed: true,
			why: []string{"CVE-2026-1 is fixed only in 5.0.3"}},
		{name: "a settled major is due for a product endoflife.date does not track", toLine: "5", lines: settled, allowed: true,
			why: []string{"5.0 was released 2026-05-01; 2 releases since"}, unknown: []string{"endoflife.date does not track django"}},
		{name: "one release after the first is not settled", toLine: "5", lines: packagefacts.Lines{Newest: "5", First: settled.First, Since: 1}, support: supported},
		{name: "an unreadable endoflife.date allows nothing and is unknown", toLine: "5", lines: young, supportErr: offline,
			why: []string{"Keep django on 4"}, unknown: []string{"end of support: dial tcp: connection refused"}},
		{name: "an unreadable registry allows nothing, even past end of support", toLine: "5", linesErr: offline, support: ended,
			unknown: []string{"the registry could not be read: dial tcp"}},
		{name: "a move short of the newest major is refused", toLine: "5", lines: packagefacts.Lines{Newest: "6", First: settled.First, Since: 9}, support: ended,
			why: []string{"a major move goes to the newest major, 6"}},
		{name: "asking without a target decides the newest", lines: young, support: ended, allowed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := decideMajor(now, django, c.toLine, c.lines, c.linesErr, c.support, c.supportErr, c.findings)
			text := got.Text()
			if got.Allowed != c.allowed {
				t.Fatalf("allowed=%t: %s", got.Allowed, text)
			}
			for _, w := range c.why {
				if !strings.Contains(got.Why, w) {
					t.Errorf("why lacks %q: %s", w, text)
				}
			}
			for _, w := range c.unknown {
				if !strings.Contains(strings.Join(got.Unknown, "; "), w) {
					t.Errorf("unknown lacks %q: %s", w, text)
				}
			}
		})
	}
}

// An ended minor beside a supported minor of the same major does not end the
// major's support (review R1): Django 5.1 ended while 5.2 is supported.
func TestAnEndedMinorDoesNotEndItsMajor(t *testing.T) {
	t.Parallel()
	dep := packagefacts.Dependency{Package: packagefacts.Package{Type: packagefacts.PyPI, Name: "django"}, Version: "5.1.4"}
	lines := packagefacts.Lines{Newest: "6", First: packagefacts.Release{Version: "6.0", Published: majorDay("2025-12-03")}, Since: 0}
	support := packagefacts.Support{Tracked: true, Product: "django", Cycles: []packagefacts.Cycle{{Name: "6.0", Ends: majorDay("2027-04-30")}, {Name: "5.2", Ends: majorDay("2028-04-30")}, {Name: "5.1", Ends: majorDay("2025-12-03"), Ended: true}}}
	got := decideMajor(majorDay("2025-12-15"), dep, "6", lines, nil, support, nil, nil)
	if got.Allowed || !strings.Contains(got.Why, "5.2 is supported until 2028-04-30") {
		t.Fatal(got.Text())
	}
}

func TestMovesBetweenListsDirectLineChanges(t *testing.T) {
	t.Parallel()
	dep := func(typ, name, version string) packagefacts.Dependency {
		return packagefacts.Dependency{Package: packagefacts.Package{Type: typ, Name: name}, Version: version, Manifest: "m"}
	}
	base := []packagefacts.Dependency{dep("npm", "react", "18.3.1"), dep("npm", "vite", "5.4.0"), dep("npm", "tiny", "0.3.1"), dep("golang", "github.com/x/y", "v1.2.0")}
	candidate := []packagefacts.Dependency{dep("npm", "react", "19.2.0"), dep("npm", "vite", "5.4.9"), dep("npm", "tiny", "0.4.0"), dep("golang", "github.com/x/y/v2", "v2.0.1"), dep("npm", "new", "3.0.0")}
	var got []string
	for _, m := range MovesBetween(base, candidate) {
		got = append(got, m.Package.Name+" "+m.FromLine+"→"+m.ToLine)
	}
	if strings.Join(got, ", ") != "github.com/x/y 1→2, react 18→19, tiny 0.3→0.4" {
		t.Fatal(got)
	}
	// Several versions at once (uv marker branches): a patch move beside a
	// held higher major is no move; a new major with no departed version to
	// explain it moves from every version on a lower line.
	moves := func(base, candidate []packagefacts.Dependency) string {
		var out []string
		for _, m := range MovesBetween(base, candidate) {
			out = append(out, m.From+"→"+m.To)
		}
		return strings.Join(out, ", ")
	}
	if got := moves([]packagefacts.Dependency{dep("pypi", "numpy", "1.26.3"), dep("pypi", "numpy", "2.0.1")}, []packagefacts.Dependency{dep("pypi", "numpy", "1.26.4"), dep("pypi", "numpy", "2.0.1")}); got != "" {
		t.Errorf("a patch move beside a held major: %s", got)
	}
	if got := moves([]packagefacts.Dependency{dep("pypi", "numpy", "1.25.0"), dep("pypi", "numpy", "1.26.4")}, []packagefacts.Dependency{dep("pypi", "numpy", "1.25.0"), dep("pypi", "numpy", "1.26.4"), dep("pypi", "numpy", "2.0.2")}); got != "1.25.0→2.0.2, 1.26.4→2.0.2" {
		t.Errorf("a new major branch: %s", got)
	}
}
