package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// janitorKinds are the Janitor's queues, in the order the page lists them.
var janitorKinds = []string{"packs", "updates", "security"}

// janitorCounts places every attempt opened in the period in exactly one
// column. Closed attempts are placed by their recorded outcome; open ones by
// the queue state recorded for Needs you, because an open attempt's outcome
// is a history marker, not what it is doing now.
type janitorCounts struct {
	Runs            int `json:"runs"`
	Shipped         int `json:"shipped"`
	NothingToChange int `json:"nothingToChange"`
	Waiting         int `json:"waiting"`
	Failed          int `json:"failed"`
	Closed          int `json:"closed"`
	Running         int `json:"running"`
}

// janitorPatchTimes summarises conformance.PatchTime units; durations are in
// seconds and mean nothing when their count is zero.
type janitorPatchTimes struct {
	Patched    int   `json:"patched"`
	Median     int64 `json:"median"`
	Slowest    int64 `json:"slowest"`
	Open       int   `json:"open"`
	OldestOpen int64 `json:"oldestOpen"`
}

type janitorKindStatistics struct {
	Kind   string        `json:"kind"`
	Counts janitorCounts `json:"counts"`
}

type janitorProjectStatistics struct {
	ProjectID ids.ID            `json:"projectId"`
	Slug      string            `json:"slug"`
	Name      string            `json:"name"`
	Counts    janitorCounts     `json:"counts"`
	Patch     janitorPatchTimes `json:"patch"`
}

// janitorReasonExample is one attempt behind a reason. ChangeID is empty once
// the attempt's Change was purged; the attempt record remains.
type janitorReasonExample struct {
	ChangeID ids.ID    `json:"changeId,omitzero"`
	Project  string    `json:"project"`
	Slug     string    `json:"slug"`
	At       time.Time `json:"at"`
}

// janitorReason groups waiting or failed attempts of one kind by the step that
// stopped them: the recorded reason up to its first ": ", or the whole text.
type janitorReason struct {
	Kind     string                 `json:"kind"`
	Column   string                 `json:"column"`
	Step     string                 `json:"step"`
	Count    int                    `json:"count"`
	Latest   string                 `json:"latest"`
	Examples []janitorReasonExample `json:"examples"`
}

type janitorStatistics struct {
	Days     int                        `json:"days"`
	Since    time.Time                  `json:"since"`
	Kinds    []janitorKindStatistics    `json:"kinds"`
	Reasons  []janitorReason            `json:"reasons"`
	Patch    janitorPatchTimes          `json:"patch"`
	Projects []janitorProjectStatistics `json:"projects"`
}

const (
	janitorWaiting          = "waiting"
	janitorFailed           = "failed"
	janitorWaitsAcceptance  = "Waiting for acceptance"
	janitorReasonsShown     = 5
	janitorExamplesPerGroup = 3
)

var errJanitorPeriod = fault.New(fault.Invalid, "the period is 7 or 30 days")

// JanitorStatistics reads the caller-visible Janitor projects' attempts and
// patch times for the last days (7 or 30): one project read, one attempt read
// and one scan read, whatever the number of projects.
func (s *Service) JanitorStatistics(ctx context.Context, days int) (janitorStatistics, error) {
	if days != 7 && days != 30 {
		return janitorStatistics{}, errJanitorPeriod
	}
	since := s.now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	out := janitorStatistics{Days: days, Since: since, Kinds: []janitorKindStatistics{}, Reasons: []janitorReason{}, Projects: []janitorProjectStatistics{}}
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return out, err
	}
	q := workflowsql.New(s.db.SQL())
	records, err := q.JanitorRecords(ctx, visible)
	if err != nil {
		return out, err
	}
	set := make([]ids.ID, 0, len(records))
	projects := map[ids.ID]*janitorProjectStatistics{}
	queues := map[ids.ID][]QueueStatus{}
	for _, r := range records {
		set = append(set, r.ID)
		projects[r.ID] = &janitorProjectStatistics{ProjectID: r.ID, Slug: r.Slug, Name: r.Name}
		var recorded []QueueStatus
		if err := json.Unmarshal(r.Queues, &recorded); err != nil {
			return out, err
		}
		queues[r.ID] = recorded
	}
	attempts, err := q.JanitorAttemptStats(ctx, workflowsql.JanitorAttemptStatsParams{ProjectIds: set, Since: since})
	if err != nil {
		return out, err
	}
	kinds := map[string]*janitorCounts{}
	for _, kind := range janitorKinds {
		kinds[kind] = &janitorCounts{}
	}
	groups := map[[3]string]*janitorReason{}
	order := [][3]string{}
	for _, a := range attempts {
		column, reason, err := janitorColumn(a, queues[a.ProjectID])
		if err != nil {
			return out, err
		}
		p := projects[a.ProjectID]
		kinds[a.Queue].add(column)
		p.Counts.add(column)
		if column != janitorWaiting && column != janitorFailed {
			continue
		}
		if reason = strings.TrimSpace(reason); reason == "" {
			reason = "No reason recorded"
		}
		step := reason
		if i := strings.Index(reason, ": "); i > 0 {
			step = reason[:i]
		}
		key := [3]string{a.Queue, column, step}
		g := groups[key]
		if g == nil {
			// Attempts arrive newest first, so the first is the latest.
			g = &janitorReason{Kind: a.Queue, Column: column, Step: step, Latest: reason, Examples: []janitorReasonExample{}}
			groups[key] = g
			order = append(order, key)
		}
		g.Count++
		if len(g.Examples) < janitorExamplesPerGroup {
			g.Examples = append(g.Examples, janitorReasonExample{ChangeID: a.ActiveChangeID, Project: p.Name, Slug: p.Slug, At: a.OpenedAt})
		}
	}
	for _, kind := range janitorKinds {
		out.Kinds = append(out.Kinds, janitorKindStatistics{Kind: kind, Counts: *kinds[kind]})
	}
	shown := map[[2]string]int{}
	reasons := make([]*janitorReason, 0, len(order))
	for _, key := range order {
		reasons = append(reasons, groups[key])
	}
	// Stable: equal counts keep newest-first order.
	slices.SortStableFunc(reasons, func(a, b *janitorReason) int { return b.Count - a.Count })
	for _, kind := range janitorKinds {
		for _, column := range []string{janitorWaiting, janitorFailed} {
			for _, r := range reasons {
				if r.Kind == kind && r.Column == column && shown[[2]string{kind, column}] < janitorReasonsShown {
					shown[[2]string{kind, column}]++
					out.Reasons = append(out.Reasons, *r)
				}
			}
		}
	}

	units, err := conformance.PatchTimesIn(ctx, s.db, set, since)
	if err != nil {
		return out, err
	}
	now := s.now()
	all := patchClock{}
	byProject := map[ids.ID]*patchClock{}
	for _, u := range units {
		c := byProject[u.ProjectID]
		if c == nil {
			c = &patchClock{}
			byProject[u.ProjectID] = c
		}
		c.add(u, now)
		all.add(u, now)
	}
	out.Patch = all.summary()
	for _, r := range records {
		p := projects[r.ID]
		if c := byProject[r.ID]; c != nil {
			p.Patch = c.summary()
		}
		// A project with nothing in the period adds a row of zeros and no answer.
		if p.Counts.Runs == 0 && p.Patch.Patched == 0 && p.Patch.Open == 0 {
			continue
		}
		out.Projects = append(out.Projects, *p)
	}
	return out, nil
}

func (c *janitorCounts) add(column string) {
	c.Runs++
	switch column {
	case "shipped":
		c.Shipped++
	case "nothing":
		c.NothingToChange++
	case janitorWaiting:
		c.Waiting++
	case janitorFailed:
		c.Failed++
	case "closed":
		c.Closed++
	default:
		c.Running++
	}
}

// janitorColumn places one attempt and returns the reason a waiting or failed
// attempt shows. An unknown closed outcome is an error: a new outcome must be
// placed here, not counted silently in some column.
func janitorColumn(a workflowsql.JanitorAttemptStatsRow, recorded []QueueStatus) (string, string, error) {
	if !a.EndedAt.Valid {
		for _, q := range recorded {
			if q.Queue != a.Queue || q.State != "decide" || a.ActiveChangeID.IsZero() || q.ChangeID != a.ActiveChangeID {
				continue
			}
			if q.Decide != nil && q.Decide.Act == NextApprove {
				return janitorWaiting, janitorWaitsAcceptance, nil
			}
			return janitorWaiting, a.Reason, nil
		}
		return "running", "", nil
	}
	switch a.Outcome {
	case "shipped":
		return "shipped", "", nil
	case "pass":
		return "nothing", "", nil
	case "held", "faulted", "retried", "fail", "error":
		return janitorFailed, a.Reason, nil
	case "skipped", "abandoned":
		return "closed", "", nil
	}
	return "", "", fmt.Errorf("janitor attempt %s has outcome %q, which the statistics do not place", a.ID, a.Outcome)
}

type patchClock struct {
	patched []time.Duration
	open    int
	oldest  time.Duration
}

func (c *patchClock) add(u conformance.PatchTime, now time.Time) {
	if u.Patched {
		c.patched = append(c.patched, u.PatchedAt.Sub(u.FirstSeen))
		return
	}
	c.open++
	c.oldest = max(c.oldest, now.Sub(u.FirstSeen))
}

func (c *patchClock) summary() janitorPatchTimes {
	out := janitorPatchTimes{Patched: len(c.patched), Open: c.open, OldestOpen: int64(c.oldest.Seconds())}
	if len(c.patched) == 0 {
		return out
	}
	slices.Sort(c.patched)
	n := len(c.patched)
	median := c.patched[n/2]
	if n%2 == 0 {
		median = (c.patched[n/2-1] + c.patched[n/2]) / 2
	}
	out.Median, out.Slowest = int64(median.Seconds()), int64(c.patched[n-1].Seconds())
	return out
}
