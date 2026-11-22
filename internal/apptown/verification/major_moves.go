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

// MajorFacts reads what the rules need: a package registry's stable lines,
// grouped by the given line, and endoflife.date's support cycles
// (packagefacts.Client).
type MajorFacts interface {
	Lines(context.Context, packagefacts.Package, string, func(string) string) (packagefacts.Lines, error)
	Support(context.Context, packagefacts.Package) (packagefacts.Support, error)
}

// releaseLine is the line a version is held to: endoflife.date's cycle for a
// product it tracks (PHP 8.3, Python 3.12, Node 24, Django 5.2); the semver
// line for a package it does not track; otherwise the narrowest line, x.y,
// with why the line is unknown. An image endoflife.date does not track has
// an unknown line: AppTown cannot tell a runtime's cycle from its tag.
func releaseLine(p packagefacts.Package, s packagefacts.Support, err error, version string) (line, unknown string) {
	switch {
	case err != nil:
		return packagefacts.Narrow(version), "the release line of " + p.Name + " is unknown: " + err.Error()
	case s.Tracked:
		if c, ok := s.CycleOf(version); ok {
			return c.Name, ""
		}
		return packagefacts.Narrow(version), fmt.Sprintf("the release line of %s is unknown: endoflife.date lists no %s cycle for %s", p.Name, s.Product, version)
	case p.Type == packagefacts.Docker:
		return packagefacts.Narrow(version), "the release line of " + p.Name + " is unknown: endoflife.date does not track it"
	}
	return packagefacts.Line(version), ""
}

// lineSource names where a tracked line comes from, for reasons.
func lineSource(s packagefacts.Support, err error) string {
	if err == nil && s.Tracked {
		return " (endoflife.date " + s.Product + ")"
	}
	return ""
}

// onLine says whether a version is on a line (8.3.36 and 8.3 are on 8.3).
func onLine(version, line string) bool {
	return version == line || strings.HasPrefix(version, line+".")
}

// supports reads and remembers endoflife.date's answer per package for one
// decision, so a run reads each product once.
type supports struct {
	facts MajorFacts
	read  map[string]supportReading
}
type supportReading struct {
	support packagefacts.Support
	err     error
}

func (s *supports) of(ctx context.Context, p packagefacts.Package) (packagefacts.Support, error) {
	if s.read == nil {
		s.read = map[string]supportReading{}
	}
	if r, ok := s.read[p.Identity()]; ok {
		return r.support, r.err
	}
	var r supportReading
	if s.facts == nil {
		r.err = fmt.Errorf("endoflife.date facts are unavailable")
	} else {
		r.support, r.err = s.facts.Support(ctx, p)
		if r.err != nil {
			r.err = fmt.Errorf("endoflife.date could not be read: %w", r.err)
		}
	}
	s.read[p.Identity()] = r
	return r.support, r.err
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
	// LineUnknown says why the release line could not be read; the
	// narrowest line, x.y, was used.
	LineUnknown string `json:"lineUnknown,omitempty"`
	// Routine marks a decision at the Routine level, where every move to
	// another line is refused.
	Routine bool `json:"routine,omitempty"`
}

// Text is the decision in one line, as the change and the agent show it.
func (m MajorMove) Text() string {
	to := m.ToLine
	if to == "" {
		to = m.Newest
	}
	name := m.Package.Name
	if m.Package.Type == packagefacts.Docker {
		name = strings.TrimPrefix(name, "library/")
	}
	head := fmt.Sprintf("%s %s → %s", name, m.FromLine, to)
	if to == "" {
		head = fmt.Sprintf("%s %s", name, m.FromLine)
	}
	verdict := "not due"
	switch {
	case m.Allowed && m.FromLine == m.ToLine:
		verdict = "stays on its line"
	case m.Allowed:
		verdict = "due"
	case m.Routine:
		verdict = "not allowed at Routine"
	}
	out := head + " " + verdict + ": " + m.Why
	unknown := m.Unknown
	if m.LineUnknown != "" {
		unknown = append([]string{m.LineUnknown}, unknown...)
	}
	if len(unknown) > 0 {
		out += " Unknown: " + strings.Join(unknown, "; ") + "."
	}
	return out
}

// decideMajor applies the rules to one dependency at now. toLine is the
// line the change moves to, or empty to ask about the newest line. A fact
// passed with its read error is unknown and allows nothing.
// line is the release line of a version (releaseLine).
func decideMajor(now time.Time, dep packagefacts.Dependency, toLine string, line func(string) string, lines packagefacts.Lines, linesErr error, support packagefacts.Support, supportErr error, findings []check.Finding) MajorMove {
	m := MajorMove{Package: dep.Package, Manifest: dep.Manifest, From: dep.Version, FromLine: line(dep.Version), ToLine: toLine}
	if linesErr != nil {
		source := "the registry could not be read: "
		if dep.Type == packagefacts.Docker {
			source = "the newest line could not be read: "
		}
		m.Unknown = append(m.Unknown, source+linesErr.Error())
		m.Why = "AppTown could not read which major is newest. Keep " + dep.Name + " on " + m.FromLine + "."
		return m
	}
	m.Newest = lines.Newest
	if toLine == "" {
		toLine = lines.Newest
	}
	if !packagefacts.LineLess(m.FromLine, lines.Newest) {
		m.Why = m.FromLine + " is the newest line."
		return m
	}
	if toLine != lines.Newest {
		m.Why = fmt.Sprintf("a major move goes to the newest line, %s. Keep %s on %s or move it to %s.", lines.Newest, dep.Name, m.FromLine, lines.Newest)
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
		// The line ends support when every cycle on it does: its last
		// cycle's date decides. For a tracked product the line is one
		// cycle. A cycle with no date and not ended is open.
		var ends time.Time
		var last string
		open, cycles, allEnded := false, 0, true
		for _, c := range support.Cycles {
			if line(c.Name) != m.FromLine {
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
		if packagefacts.LineLess(m.FromLine, line(p.FixedIn)) {
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
	case lines.Since < 0:
		m.Unknown = append(m.Unknown, fmt.Sprintf("how many releases followed %s (released %s): endoflife.date does not say", lines.First.Version, lines.First.Published.Format(time.DateOnly)))
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

// movesBetween lists the direct dependencies that move to a higher line
// between two trees' dependency lists, keyed by manifest and package
// identity; line gives a package version's line (releaseLine). A package may
// be held at several versions at once (a uv lock's marker branches), so
// versions are compared as sets, and a move always starts from a version that
// was installed before:
//   - a version that left, whose line no newly arrived version took over,
//     moved to the highest version on a higher line;
//   - a version that arrived on a line nothing held before, and that no
//     departed version explains, moved from each version held on a lower
//     line, since the lock does not say which branch it replaced; each must
//     be due.
func movesBetween(base, candidate []packagefacts.Dependency, line func(packagefacts.Package, string) string) []MajorMove {
	type versions map[string]packagefacts.Dependency
	index := func(deps []packagefacts.Dependency) (map[string]versions, []string) {
		out, order := map[string]versions{}, []string(nil)
		for _, d := range deps {
			if line(d.Package, d.Version) == "" {
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
	for _, key := range order {
		b, a := before[key], after[key]
		if b == nil {
			continue
		}
		var pkg packagefacts.Package
		for _, d := range b {
			pkg = d.Package
			break
		}
		lineOf := func(v string) string { return line(pkg, v) }
		add := func(from, to packagefacts.Dependency) {
			f, t := lineOf(from.Version), lineOf(to.Version)
			if key := from.Version + "\x00" + from.Identity() + "\x00" + from.Manifest + "\x00" + t; !seen[key] {
				seen[key] = true
				out = append(out, MajorMove{Package: from.Package, Manifest: from.Manifest, From: from.Version, To: to.Version, FromLine: f, ToLine: t})
			}
		}
		baseLines, arrivedLines, explained := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for v := range b {
			baseLines[lineOf(v)] = true
		}
		for v := range a {
			if _, had := b[v]; !had {
				arrivedLines[lineOf(v)] = true
			}
		}
		for v, from := range b {
			if _, kept := a[v]; kept || arrivedLines[lineOf(v)] {
				continue
			}
			var top packagefacts.Dependency
			for w, to := range a {
				if packagefacts.LineLess(lineOf(v), lineOf(w)) && (top.Version == "" || packagefacts.Less(top.Version, w)) {
					top = to
				}
			}
			if top.Version != "" {
				add(from, top)
				explained[top.Version] = true
			}
		}
		for w, to := range a {
			if _, had := b[w]; had || baseLines[lineOf(w)] || explained[w] {
				continue
			}
			for v, from := range b {
				if packagefacts.LineLess(lineOf(v), lineOf(w)) {
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

// narrowLine is the narrowest line any package can be on: every move to
// another release line also leaves it.
func narrowLine(_ packagefacts.Package, v string) string { return packagefacts.Narrow(v) }

// Check decides the direct dependencies a change moves to another release
// line. At Routine every such move is refused; at Including major
// (aggressive) each is decided by the rules. endoflife.date is read only for
// packages whose x.y moved, once each.
func (m Majors) Check(ctx context.Context, project ids.ID, base, candidate []packagefacts.Dependency, aggressive bool) ([]MajorMove, error) {
	narrow := movesBetween(base, candidate, narrowLine)
	if len(narrow) == 0 {
		return nil, nil
	}
	read := &supports{facts: m.Facts}
	for _, mv := range narrow {
		read.of(ctx, mv.Package)
	}
	line := func(p packagefacts.Package, v string) string {
		s, err := read.of(ctx, p)
		l, _ := releaseLine(p, s, err, v)
		return l
	}
	moves := movesBetween(base, candidate, line)
	if aggressive {
		return m.decide(ctx, project, moves, read)
	}
	for i, mv := range moves {
		s, err := read.of(ctx, mv.Package)
		_, unknown := releaseLine(mv.Package, s, err, mv.From)
		mv.Routine, mv.LineUnknown = true, unknown
		mv.Why = fmt.Sprintf("%s leaves its line %s%s; Routine keeps it on %s.", mv.To, mv.FromLine, lineSource(s, err), mv.FromLine)
		moves[i] = mv
	}
	return moves, nil
}

// Images gives image updates the same facts and clock, so an image's
// release line is read and decided like a package's.
func (m Majors) Images(u ImageUpdates) ImageUpdates {
	u.facts, u.now = m.Facts, m.Now
	return u
}

// Decide answers each move: a MajorMove from movesBetween, or one with only
// Package and From to ask about the newest line.
func (m Majors) Decide(ctx context.Context, project ids.ID, moves []MajorMove) ([]MajorMove, error) {
	return m.decide(ctx, project, moves, &supports{facts: m.Facts})
}

func (m Majors) decide(ctx context.Context, project ids.ID, moves []MajorMove, read *supports) ([]MajorMove, error) {
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
		d := decideLine(ctx, now(), m.Facts, read, mv, findings)
		d.To = mv.To
		out = append(out, d)
	}
	return out, nil
}

// decideLine applies the rules to one move on its release lines: a
// package's registry history grouped by its line, or an image's
// endoflife.date cycles.
func decideLine(ctx context.Context, now time.Time, facts MajorFacts, read *supports, mv MajorMove, findings []check.Finding) MajorMove {
	dep := packagefacts.Dependency{Package: mv.Package, Version: mv.From, Manifest: mv.Manifest}
	support, supportErr := read.of(ctx, mv.Package)
	line := func(v string) string { l, _ := releaseLine(mv.Package, support, supportErr, v); return l }
	_, unknown := releaseLine(mv.Package, support, supportErr, mv.From)
	var lines packagefacts.Lines
	var linesErr error
	switch {
	case mv.Package.Type == packagefacts.Docker && supportErr != nil:
		linesErr = supportErr
	case mv.Package.Type == packagefacts.Docker && !support.Tracked:
		linesErr = fmt.Errorf("endoflife.date does not track %s", mv.Package.Name)
	case mv.Package.Type == packagefacts.Docker:
		lines, linesErr = packagefacts.CycleLines(support)
	case facts == nil:
		linesErr = fmt.Errorf("package registry facts are unavailable")
	default:
		lines, linesErr = facts.Lines(ctx, mv.Package, mv.From, line)
	}
	// decideMajor names an unreadable endoflife.date itself.
	d := decideMajor(now, dep, mv.ToLine, line, lines, linesErr, support, supportErr, findings)
	d.LineUnknown = unknown
	return d
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
