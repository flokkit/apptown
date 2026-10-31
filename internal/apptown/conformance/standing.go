package conformance

import (
	"fmt"
	"sort"

	"github.com/flokkit/apptown/internal/ids"
)

type Lanes struct {
	Conforms   int `json:"conforms"`
	Excepted   int `json:"excepted"`
	Findings   int `json:"findings"`   // does-not-conform: deterministic, or AI confirmed by a person
	Flagged    int `json:"flagged"`    // does-not-conform by AI, not yet confirmed
	Unassessed int `json:"unassessed"` // cannot-assess, not-scanned, stale
	Broken     int `json:"broken"`     // error — the evaluator failed, not the project
	NA         int `json:"na"`         // transition checks under a review lens (not in the judged denominator)
}

// Standing is computed from the adopted checks; it is never a mutable lifecycle.
type Standing string

const (
	StandingDoesNot   Standing = "does-not-conform"
	StandingBroken    Standing = "review-broken"
	StandingNeedsLook Standing = "needs-a-look"
	StandingSoFar     Standing = "conforms-so-far"
	StandingExcepted  Standing = "conforms-with-exceptions"
	StandingConforms  Standing = "conforms"
	StandingNothing   Standing = "nothing-adopted"
)

type ProjectStanding struct {
	ProjectID   ids.ID     `json:"projectId"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"` // the name people know the project by
	StatusLabel CheckLabel `json:"statusLabel"`
	Standing    Standing   `json:"standing"`
	Sentence    string     `json:"sentence"`
	Lanes       Lanes      `json:"lanes"`
	Total       int        `json:"total"` // M: adopted checks excluding n/a
	Judged      int        `json:"judged"`
	High        int        `json:"high"` // findings at severity high
	Adopted     []string   `json:"adopted"`
	SHA         string     `json:"sha"`
	JudgedAt    string     `json:"judgedAt"`
}

// Adjudication is a human confirmation of one immutable verdict.
type Adjudication struct {
	ID        ids.ID `json:"id"`
	VerdictID ids.ID `json:"verdictId"`
	Kind      string `json:"kind"` // confirmed
	By        string `json:"by"`
	// ByPrincipal is the confirmer's principal; zero when not resolved to a person.
	ByPrincipal ids.ID `json:"byPrincipal,omitzero"`
	Note        string `json:"note,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

type PriorConfirmation struct {
	SHA string `json:"sha"`
	By  string `json:"by"`
	At  string `json:"at"`
}

type HistoryPoint struct {
	ReviewID   ids.ID   `json:"reviewId"`
	At         string   `json:"at"`
	Findings   int      `json:"findings"`
	Flagged    int      `json:"flagged"`
	Excepted   int      `json:"excepted"`
	Unassessed int      `json:"unassessed"`
	Broken     int      `json:"broken"`
	Total      int      `json:"total"`
	Standing   Standing `json:"standing"`
}

// LaneOf interprets a saved finding and its exact confirmation. Absence never
// becomes an actionable finding, even on an incompletely constructed observation.
func LaneOf(v Observation, adj map[ids.ID]Adjudication) Lane {
	if v.Record == nil {
		return LaneUnassessed
	}
	switch v.State {
	case Conforms:
		return LaneConforms
	case Excepted:
		return LaneExcepted
	case DoesNotConform:
		if v.Record != nil && v.Record.Evaluator == "llm" {
			if _, ok := adj[v.Record.ID]; ok {
				return LaneFindings
			}
			return LaneFlagged
		}
		return LaneFindings
	case NotApplicable:
		return LaneNotApplicable
	case EvaluationError:
		return LaneBroken
	default: // cannot-assess, not-scanned, stale
		return LaneUnassessed
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func standingFor(l Lanes, high, total int) (Standing, string) {
	switch {
	case total == 0:
		return StandingNothing, "Nothing adopted"
	case l.Findings > 0:
		s := "Does not conform · " + plural(l.Findings, "finding", "findings")
		if high > 0 {
			s += fmt.Sprintf(" (%d high)", high)
		}
		if l.Broken > 0 {
			s += fmt.Sprintf(" · %d couldn't run", l.Broken)
		}
		return StandingDoesNot, s
	case l.Broken > 0:
		return StandingBroken, "Review incomplete · " + plural(l.Broken, "check couldn't run", "checks couldn't run")
	case l.Flagged > 0:
		return StandingNeedsLook, "Needs a look · " + plural(l.Flagged, "AI flag", "AI flags")
	case l.Unassessed > 0:
		sentence := plural(l.Conforms, "check passed", "checks passed")
		if l.Excepted > 0 {
			sentence += " · " + plural(l.Excepted, "accepted exception", "accepted exceptions")
		}
		return StandingSoFar, sentence + " · " + plural(l.Unassessed, "not yet assessed", "not yet assessed")
	case l.Excepted > 0:
		return StandingExcepted, "Meets adopted standards · " + plural(l.Excepted, "accepted exception", "accepted exceptions")
	default:
		return StandingConforms, "Meets adopted standards · " + plural(l.Conforms, "check passed", "checks passed")
	}
}

func StandingRank(s Standing) int {
	switch s {
	case StandingDoesNot:
		return 0
	case StandingBroken:
		return 1
	case StandingNeedsLook:
		return 2
	case StandingSoFar:
		return 3
	case StandingExcepted:
		return 4
	case StandingConforms:
		return 5
	default:
		return 6
	}
}

// SummarizeProject counts each adopted check once and derives standing from its
// interpretation. Saved evidence supplies the latest judged SHA/time.
func SummarizeProject(projectID ids.ID, slug string, vs []Observation, adj map[ids.ID]Adjudication) ProjectStanding {
	ps := ProjectStanding{ProjectID: projectID, Slug: slug}
	adopted := map[string]bool{}
	for _, v := range vs {
		if v.Block != "" {
			adopted[fmt.Sprintf("%s@%d", v.Block, v.Version)] = true
		}
		switch LaneOf(v, adj) {
		case LaneConforms:
			ps.Lanes.Conforms++
		case LaneExcepted:
			ps.Lanes.Excepted++
		case LaneFindings:
			ps.Lanes.Findings++
			if v.Record != nil && v.Record.Severity == "high" {
				ps.High++
			}
		case LaneFlagged:
			ps.Lanes.Flagged++
		case LaneNotApplicable:
			ps.Lanes.NA++
		case LaneBroken:
			ps.Lanes.Broken++
		default:
			ps.Lanes.Unassessed++
		}
		if v.Record != nil && v.Record.SHA != "" && (ps.JudgedAt == "" || v.Record.At > ps.JudgedAt) {
			ps.SHA, ps.JudgedAt = v.Record.SHA, v.Record.At
		}
	}
	for k := range adopted {
		ps.Adopted = append(ps.Adopted, k)
	}
	sort.Strings(ps.Adopted)
	ps.Total = len(vs) - ps.Lanes.NA
	ps.Judged = ps.Total - ps.Lanes.Unassessed
	ps.Standing, ps.Sentence = standingFor(ps.Lanes, ps.High, ps.Total)
	ps.StatusLabel = ps.Standing.Label()
	return ps
}

func worklistRank(state ObservationState) int {
	switch state {
	case DoesNotConform:
		return 0
	case EvaluationError:
		return 1
	case CannotAssess, Stale:
		return 2
	default:
		return 3
	}
}

// GroupByProject orders each project worklist with actionable/unknown results first.
func GroupByProject(vs []Observation) map[string][]Observation {
	out := map[string][]Observation{}
	for _, v := range vs {
		out[v.ProjectSlug] = append(out[v.ProjectSlug], v)
	}
	for k := range out {
		sortVerdicts(out[k])
	}
	return out
}

// GroupByCheck applies the same worklist order across projects.
func GroupByCheck(vs []Observation) map[string][]Observation {
	out := map[string][]Observation{}
	for _, v := range vs {
		out[CheckKey(v.Block, v.Version, v.Check)] = append(out[CheckKey(v.Block, v.Version, v.Check)], v)
	}
	for k := range out {
		sortVerdicts(out[k])
	}
	return out
}

func sortVerdicts(vs []Observation) {
	sort.SliceStable(vs, func(i, j int) bool {
		if ri, rj := worklistRank(vs[i].State), worklistRank(vs[j].State); ri != rj {
			return ri < rj
		}
		return vs[i].ProjectSlug < vs[j].ProjectSlug
	})
}

func CheckKey(block string, version int, check string) string {
	return fmt.Sprintf("%s@%d·%s", block, version, check)
}

type Lane uint8

const (
	LaneConforms Lane = iota + 1
	LaneExcepted
	LaneFindings
	LaneFlagged
	LaneNotApplicable
	LaneBroken
	LaneUnassessed
)

// SortProjects orders the fleet by standing, then findings and project name.
func SortProjects(projects []ProjectStanding) {
	sort.Slice(projects, func(i, j int) bool {
		ri, rj := StandingRank(projects[i].Standing), StandingRank(projects[j].Standing)
		if ri != rj {
			return ri < rj
		}
		if projects[i].Lanes.Findings != projects[j].Lanes.Findings {
			return projects[i].Lanes.Findings > projects[j].Lanes.Findings
		}
		return projects[i].Slug < projects[j].Slug
	})

}
