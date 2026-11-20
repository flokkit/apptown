package verification

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/packagefacts"
)

// When a direct dependency may move to a new major at the Janitor's
// Including major level (aggressive). Staying on a supported major is the
// default; these say when a move is due. They are the owner's rule, not
// settings.
const (
	// endOfSupportNotice: a major whose support ends within this many
	// months is due to move.
	endOfSupportNotice = 3
	// settledAfter and settledReleases: the newest major has settled when
	// its first release is this many months old and this many releases have
	// followed it.
	settledAfter    = 6
	settledReleases = 2
)

// MajorFacts reads what the rules need: a package registry's stable lines
// and endoflife.date's support cycles (packagefacts.Client).
type MajorFacts interface {
	Lines(context.Context, packagefacts.Package, string) (packagefacts.Lines, error)
	Support(context.Context, packagefacts.Package) (packagefacts.Support, error)
}

// MajorMove is AppTown's decision on one direct dependency leaving its major
// (or a 0.x package leaving its minor).
type MajorMove struct {
	Package  packagefacts.Package `json:"package"`
	Manifest string               `json:"manifest,omitempty"`
	From     string               `json:"from"`
	// To is the version the change moves to; empty when the agent asks
	// before moving.
	To       string `json:"to,omitempty"`
	FromLine string `json:"fromLine"`
	ToLine   string `json:"toLine,omitempty"`
	// Newest is the registry's newest stable line, empty when unread.
	Newest  string `json:"newest,omitempty"`
	Allowed bool   `json:"allowed"`
	// Why says which rule allows the move, or why none does.
	Why string `json:"why"`
	// Unknown lists the facts that could not be read; none of them allowed
	// the move.
	Unknown []string `json:"unknown,omitempty"`
}

// Text is the decision in one line, as the change and the agent show it.
func (m MajorMove) Text() string {
	to := m.ToLine
	if to == "" {
		to = m.Newest
	}
	head := fmt.Sprintf("%s %s → %s", m.Package.Name, m.FromLine, to)
	if to == "" {
		head = fmt.Sprintf("%s %s", m.Package.Name, m.FromLine)
	}
	verdict := "not due"
	if m.Allowed {
		verdict = "due"
	}
	out := head + " " + verdict + ": " + m.Why
	if len(m.Unknown) > 0 {
		out += " Unknown: " + strings.Join(m.Unknown, "; ") + "."
	}
	return out
}

// decideMajor applies the rules to one dependency at now. toLine is the
// line the change moves to, or empty to ask about the newest line. A fact
// passed with its read error is unknown and allows nothing.
func decideMajor(now time.Time, dep packagefacts.Dependency, toLine string, lines packagefacts.Lines, linesErr error, support packagefacts.Support, supportErr error, findings []check.Finding) MajorMove {
	m := MajorMove{Package: dep.Package, Manifest: dep.Manifest, From: dep.Version, FromLine: packagefacts.Line(dep.Version), ToLine: toLine}
	if linesErr != nil {
		m.Unknown = append(m.Unknown, "the registry could not be read: "+linesErr.Error())
		m.Why = "AppTown could not read which major is newest. Keep " + dep.Name + " on " + m.FromLine + "."
		return m
	}
	m.Newest = lines.Newest
	if toLine == "" {
		toLine = lines.Newest
	}
	if !packagefacts.LineLess(m.FromLine, lines.Newest) {
		m.Why = m.FromLine + " is the newest major."
		return m
	}
	if toLine != lines.Newest {
		m.Why = fmt.Sprintf("a major move goes to the newest major, %s. Keep %s on %s or move it to %s.", lines.Newest, dep.Name, m.FromLine, lines.Newest)
		return m
	}
	var allowed, refused []string

	// Rule 1: the current major ends support.
	switch {
	case supportErr != nil:
		m.Unknown = append(m.Unknown, "end of support: "+supportErr.Error())
	case !support.Tracked:
		m.Unknown = append(m.Unknown, "endoflife.date does not track "+dep.Name)
	default:
		// The major ends support when every cycle on it does: its last
		// cycle's date decides. A cycle with no date and not ended is open.
		var ends time.Time
		var last string
		open, cycles, allEnded := false, 0, true
		for _, c := range support.Cycles {
			if packagefacts.Line(c.Name) != m.FromLine {
				continue
			}
			cycles++
			allEnded = allEnded && c.Ended
			switch {
			case c.Ends.IsZero() && !c.Ended:
				open = true
			case c.Ends.After(ends):
				ends, last = c.Ends, c.Name
			}
		}
		switch {
		case cycles == 0:
			m.Unknown = append(m.Unknown, fmt.Sprintf("endoflife.date lists no %s cycle for %s", m.FromLine, support.Product))
		case open:
			refused = append(refused, fmt.Sprintf("%s %s has no end of support date (endoflife.date)", dep.Name, m.FromLine))
		case ends.IsZero():
			allowed = append(allowed, fmt.Sprintf("%s %s is out of support (endoflife.date)", dep.Name, m.FromLine))
		case allEnded || !ends.After(now):
			allowed = append(allowed, fmt.Sprintf("%s ended support %s (endoflife.date)", last, ends.Format(time.DateOnly)))
		case !ends.After(now.AddDate(0, endOfSupportNotice, 0)):
			allowed = append(allowed, fmt.Sprintf("%s ends support %s (endoflife.date)", last, ends.Format(time.DateOnly)))
		default:
			refused = append(refused, fmt.Sprintf("%s is supported until %s (endoflife.date)", last, ends.Format(time.DateOnly)))
		}
	}
	if lines.Deprecated != "" {
		allowed = append(allowed, fmt.Sprintf("the registry deprecated %s %s: %s", dep.Name, m.FromLine, lines.Deprecated))
	}

	// Rule 2: a vulnerability is fixed only on a newer major.
	for _, f := range findings {
		p := f.Package
		if p == nil || p.FixedIn == "" || osvType(p.Ecosystem) != dep.Type {
			continue
		}
		// The finding must be on this installed version: another manifest's
		// version of the package says nothing about this one.
		if (packagefacts.Package{Type: dep.Type, Name: p.Name}).Identity() != dep.Identity() || strings.TrimPrefix(p.Version, "v") != strings.TrimPrefix(dep.Version, "v") {
			continue
		}
		if packagefacts.LineLess(m.FromLine, packagefacts.Line(p.FixedIn)) {
			id := f.Title
			if len(f.Identifiers) > 0 {
				id = f.Identifiers[0]
			}
			allowed = append(allowed, fmt.Sprintf("%s is fixed only in %s", id, p.FixedIn))
		}
	}

	// Rule 3: the newest major has settled.
	switch {
	case lines.First.Published.IsZero():
		m.Unknown = append(m.Unknown, "the registry gives no publish date for "+lines.First.Version)
	case !lines.First.Published.After(now.AddDate(0, -settledAfter, 0)) && lines.Since >= settledReleases:
		allowed = append(allowed, fmt.Sprintf("%s was released %s; %d releases since", lines.First.Version, lines.First.Published.Format(time.DateOnly), lines.Since))
	default:
		refused = append(refused, fmt.Sprintf("%s was released %s with %d releases since (due after %d months and %d releases)", lines.First.Version, lines.First.Published.Format(time.DateOnly), lines.Since, settledAfter, settledReleases))
	}

	if len(allowed) > 0 {
		m.Allowed = true
		m.Why = strings.Join(allowed, "; ") + "."
		return m
	}
	m.Why = strings.Join(append(refused, "no finding needs it"), "; ") + ". Keep " + dep.Name + " on " + m.FromLine + "."
	return m
}

// osvType maps a scanner's OSV ecosystem to a purl type.
func osvType(ecosystem string) string {
	switch strings.ToLower(ecosystem) {
	case "npm":
		return packagefacts.NPM
	case "pypi":
		return packagefacts.PyPI
	case "go", "golang":
		return packagefacts.Go
	case "rubygems", "gem":
		return packagefacts.Gem
	case "packagist", "composer":
		return packagefacts.Composer
	}
	return ""
}

// Majors decides major moves for a project's dependencies: the routine step
// enforces its decisions, and the agent asks it before moving a major.
type Majors struct {
	Facts MajorFacts
	DB    *database.DB
	Now   func() time.Time
}

// MovesBetween lists the direct dependencies that move to a higher line
// between two trees' dependency lists, keyed by manifest and package
// identity. A package may be held at several versions at once (a uv lock's
// marker branches), so versions are compared as sets, and a move always
// starts from a version that was installed before:
//   - a version that left, whose line no newly arrived version took over,
//     moved to the highest version on a higher line;
//   - a version that arrived on a line nothing held before, and that no
//     departed version explains, moved from each version held on a lower
//     line, since the lock does not say which branch it replaced; each must
//     be due.
func MovesBetween(base, candidate []packagefacts.Dependency) []MajorMove {
	type versions map[string]packagefacts.Dependency
	index := func(deps []packagefacts.Dependency) (map[string]versions, []string) {
		out, order := map[string]versions{}, []string(nil)
		for _, d := range deps {
			if packagefacts.Line(d.Version) == "" {
				continue
			}
			key := d.Manifest + "\x00" + d.Identity()
			if out[key] == nil {
				out[key] = versions{}
				order = append(order, key)
			}
			out[key][d.Version] = d
		}
		return out, order
	}
	before, _ := index(base)
	after, order := index(candidate)
	var out []MajorMove
	seen := map[string]bool{}
	add := func(from, to packagefacts.Dependency) {
		f, t := packagefacts.Line(from.Version), packagefacts.Line(to.Version)
		if key := from.Version + "\x00" + from.Identity() + "\x00" + from.Manifest + "\x00" + t; !seen[key] {
			seen[key] = true
			out = append(out, MajorMove{Package: from.Package, Manifest: from.Manifest, From: from.Version, To: to.Version, FromLine: f, ToLine: t})
		}
	}
	for _, key := range order {
		b, a := before[key], after[key]
		if b == nil {
			continue
		}
		baseLines, arrivedLines, explained := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for v := range b {
			baseLines[packagefacts.Line(v)] = true
		}
		for v := range a {
			if _, had := b[v]; !had {
				arrivedLines[packagefacts.Line(v)] = true
			}
		}
		for v, from := range b {
			if _, kept := a[v]; kept || arrivedLines[packagefacts.Line(v)] {
				continue
			}
			var top packagefacts.Dependency
			for w, to := range a {
				if packagefacts.LineLess(packagefacts.Line(v), packagefacts.Line(w)) && (top.Version == "" || packagefacts.Less(top.Version, w)) {
					top = to
				}
			}
			if top.Version != "" {
				add(from, top)
				explained[top.Version] = true
			}
		}
		for w, to := range a {
			if _, had := b[w]; had || baseLines[packagefacts.Line(w)] || explained[w] {
				continue
			}
			for v, from := range b {
				if packagefacts.LineLess(packagefacts.Line(v), packagefacts.Line(w)) {
					add(from, to)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Package.Name != out[j].Package.Name {
			return out[i].Package.Name < out[j].Package.Name
		}
		if out[i].From != out[j].From {
			return packagefacts.Less(out[i].From, out[j].From)
		}
		return packagefacts.LineLess(out[i].ToLine, out[j].ToLine)
	})
	return out
}

// Decide answers each move: a MajorMove from MovesBetween, or one with only
// Package, From and FromLine to ask about the newest major.
func (m Majors) Decide(ctx context.Context, project ids.ID, moves []MajorMove) ([]MajorMove, error) {
	if len(moves) == 0 {
		return nil, nil
	}
	findings, err := m.findings(ctx, project)
	if err != nil {
		return nil, err
	}
	now := clock.Or(m.Now)
	out := make([]MajorMove, 0, len(moves))
	for _, mv := range moves {
		dep := packagefacts.Dependency{Package: mv.Package, Version: mv.From, Manifest: mv.Manifest}
		lines, linesErr := m.Facts.Lines(ctx, mv.Package, mv.From)
		var support packagefacts.Support
		var supportErr error
		if linesErr == nil {
			support, supportErr = m.Facts.Support(ctx, mv.Package)
		}
		d := decideMajor(now(), dep, mv.ToLine, lines, linesErr, support, supportErr, findings)
		d.To = mv.To
		out = append(out, d)
	}
	return out, nil
}

// findings are the package findings of the project's current source scans,
// the same observations the security queue takes its targets from.
func (m Majors) findings(ctx context.Context, project ids.ID) ([]check.Finding, error) {
	heads, err := conformance.ScanObservationHeadsIn(ctx, m.DB, project)
	if err != nil {
		return nil, err
	}
	var selected []ids.ID
	for _, h := range heads {
		if h.Subject.On == "source" {
			selected = append(selected, h.ID)
		}
	}
	observations, err := conformance.ScanObservationsByIDIn(ctx, m.DB, project, selected)
	if err != nil {
		return nil, err
	}
	var out []check.Finding
	for _, o := range observations {
		for _, f := range o.Findings {
			if f.Package != nil {
				out = append(out, f.Finding)
			}
		}
	}
	return out, nil
}
