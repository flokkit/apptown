package evals

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Score is what one arm did, read from its copy's records. It decides
// nothing and is never evidence: a person reads it. Its approvals were
// eval-authorised, never a person's acceptance, so acceptance is not
// measured.
type Score struct {
	ReadyToLand bool   `json:"readyToLand"`
	Landed      bool   `json:"landed"`
	Deploy      string `json:"deploy,omitempty"` // deployed | failed: … | not reached
	// Checks are the landing's gate steps as they ended on the arm's work.
	Checks []CheckResult `json:"checks"`
	// RequestMet is the narrative's reading of the work against the request:
	// a model's reading, not acceptance.
	RequestMet string `json:"requestMet,omitempty"`
	RequestGap string `json:"requestGap,omitempty"`
	// Reworks is the automated rework rounds of the arm's working run.
	Reworks int `json:"reworks"`
	// WorkSeconds is from the working run's start to ready-to-land.
	WorkSeconds int64 `json:"workSeconds,omitempty"`
	// Compared is the eval's own check of the landed main against the
	// Change's base over both arms' checks, finding by finding; CompareError
	// is why it could not be made.
	Compared     *verification.ArmCompared `json:"compared,omitempty"`
	CompareError string                    `json:"compareError,omitempty"`
	// NotComparable: the arm needed what a copy cannot have, or its setup
	// changed since start; it is left out of better and worse.
	NotComparable bool `json:"notComparable,omitempty"`
	// Unreadable: the stored score could not be read, and why. Nothing else
	// in the score is a reading then: the arm is not comparable, and its
	// facts are not known rather than no, zero or empty.
	Unreadable string `json:"unreadable,omitempty"`
}

// CheckResult is one gate step of the landing.
type CheckResult struct {
	Step   string `json:"step"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

var gateSteps = map[string]bool{"tests": true, "scan": true, "guidance/conformance": true, "reconcile": true}

func (s *Service) score(ctx context.Context, a Arm, ch *workflow.Change, work, landing *workflow.Run) Score {
	sc := Score{Checks: []CheckResult{}, Landed: ch.State == workflow.ChangeShipped, RequestMet: string(ch.RequestMet), RequestGap: ch.RequestGap, Deploy: "not reached"}
	if work != nil {
		sc.ReadyToLand = work.Status == workflow.RunDone
		if snap, err := s.deps.Work.Snapshot(ctx, work.ID, workflow.DetailView); err == nil {
			for _, ev := range snap.Events {
				if ev.Kind == "step.rework" {
					sc.Reworks++
				}
			}
		}
		if a.StartedAt != nil && a.ReadyAt != nil {
			sc.WorkSeconds = int64(a.ReadyAt.Sub(*a.StartedAt) / time.Second)
		}
	}
	if landing != nil {
		if snap, err := s.deps.Work.Snapshot(ctx, landing.ID, workflow.DetailView); err == nil {
			for _, st := range snap.Steps {
				switch {
				case st.Type == "deploy":
					sc.Deploy = deployOutcome(st)
				case gateSteps[st.Type]:
					sc.Checks = append(sc.Checks, CheckResult{Step: st.Name, Type: st.Type, Status: st.Status.String()})
				}
			}
		}
	}
	return sc
}

// Scorecard is an eval with what each arm cost and what was complained
// about in it, read now: spend and complaints are recorded against the
// copies and outlive them.
type Scorecard struct {
	Eval
	// CostMicros is all the eval's spend; Incomplete counts observations
	// whose spend was unknown, and makes it "at least".
	CostMicros int64                 `json:"costMicros"`
	Incomplete int64                 `json:"incomplete"`
	ArmCosts   map[ids.ID]ArmCost    `json:"armCosts"`
	Complaints []complaint.Complaint `json:"complaints"`
	// Tallies are the variant against the baseline, measure by measure,
	// over the Comparable Changes: both arms ended and neither needed what
	// a copy cannot have or drifted from its setup. NotComparable Changes
	// are left out and counted. Observed in this run; no significance.
	Tallies       []Tally `json:"tallies"`
	Comparable    int     `json:"comparable"`
	NotComparable int     `json:"notComparable"`
}

// Tally is how the variant did against the baseline on one measure.
// Unknown counts Changes where either arm has no reading of it.
type Tally struct {
	Measure string `json:"measure"`
	Better  int    `json:"better"`
	Same    int    `json:"same"`
	Worse   int    `json:"worse"`
	Unknown int    `json:"unknown"`
}

// ArmCost is what one arm's copy spent.
type ArmCost struct {
	CostMicros int64 `json:"costMicros"`
	Tokens     int64 `json:"tokens"`
	Incomplete int64 `json:"incomplete"`
}

// Scorecard reads an eval's scorecard for someone who may read evals.
func (s *Service) Scorecard(ctx context.Context, id ids.ID) (*Scorecard, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &Scorecard{Eval: *e, ArmCosts: map[ids.ID]ArmCost{}}
	// Every copy the copy record holds counts, the arm's or one an
	// interruption left: what was spent is never shown as less.
	copies, err := queries(s.db).CopyArmsOfEval(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range copies {
		t, err := s.deps.Spend.ProjectsTotal(ctx, s.db, c.ProjectID)
		if err != nil {
			return nil, err
		}
		cost := out.ArmCosts[c.ArmID]
		cost.CostMicros, cost.Tokens, cost.Incomplete = cost.CostMicros+t.CostMicros, cost.Tokens+t.Total, cost.Incomplete+t.Incomplete
		out.ArmCosts[c.ArmID] = cost
		out.CostMicros += t.CostMicros
		out.Incomplete += t.Incomplete
	}
	if out.Complaints, err = s.deps.Complaints.OfEval(ctx, id); err != nil {
		return nil, err
	}
	out.Tallies, out.Comparable, out.NotComparable = tally(out.Arms, out.ArmCosts)
	return out, nil
}

// A measure reads one arm: its value, and whether there is one. Higher
// says which way is better.
type measure struct {
	name   string
	higher bool
	read   func(a Arm, cost ArmCost) (float64, bool)
}

var measures = []measure{
	{"landed", true, func(a Arm, _ ArmCost) (float64, bool) { return boolValue(a.Score.Landed), true }},
	{"new findings", false, func(a Arm, _ ArmCost) (float64, bool) {
		return findings(a, func(r verification.CheckRow) []string { return r.New })
	}},
	{"findings fixed", true, func(a Arm, _ ArmCost) (float64, bool) {
		return findings(a, func(r verification.CheckRow) []string { return r.Fixed })
	}},
	{"request met", true, func(a Arm, _ ArmCost) (float64, bool) {
		v, ok := map[string]float64{"yes": 2, "partly": 1, "no": 0}[a.Score.RequestMet]
		return v, ok
	}},
	{"reworks", false, func(a Arm, _ ArmCost) (float64, bool) { return float64(a.Score.Reworks), a.Score.ReadyToLand }},
	{"time to ready", false, func(a Arm, _ ArmCost) (float64, bool) { return float64(a.Score.WorkSeconds), a.Score.WorkSeconds > 0 }},
	{"cost", false, func(_ Arm, c ArmCost) (float64, bool) { return float64(c.CostMicros), c.Incomplete == 0 }},
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// findings counts the arm's compared findings of one kind. Without a
// comparison there is no reading, and a check whose findings could not be
// compared (it errored, could not run, or failed on both trees with no
// occurrences) leaves the count unknown, never zero (E2B-02).
func findings(a Arm, of func(verification.CheckRow) []string) (float64, bool) {
	if a.Score.Compared == nil {
		return 0, false
	}
	n := 0
	for _, r := range a.Score.Compared.Rows {
		if r.NotComparable != "" {
			return 0, false
		}
		n += len(of(r))
	}
	return float64(n), true
}

// tally compares the arms of each Change where both ended and both are
// comparable.
func tally(arms []Arm, costs map[ids.ID]ArmCost) (out []Tally, comparable, notComparable int) {
	type pair struct{ baseline, variant *Arm }
	byChange := map[ids.ID]*pair{}
	var order []ids.ID
	for i := range arms {
		a := &arms[i]
		p := byChange[a.SourceChangeID]
		if p == nil {
			p = &pair{}
			byChange[a.SourceChangeID] = p
			order = append(order, a.SourceChangeID)
		}
		if a.Arm == ArmBaseline {
			p.baseline = a
		} else {
			p.variant = a
		}
	}
	out = make([]Tally, len(measures))
	for i, m := range measures {
		out[i].Measure = m.name
	}
	for _, id := range order {
		p := byChange[id]
		switch {
		case p.baseline == nil || p.variant == nil || !p.baseline.terminal() || !p.variant.terminal():
			continue
		case p.baseline.Score.NotComparable || p.variant.Score.NotComparable:
			notComparable++
			continue
		}
		comparable++
		for i, m := range measures {
			b, bok := m.read(*p.baseline, costs[p.baseline.ID])
			v, vok := m.read(*p.variant, costs[p.variant.ID])
			switch {
			case !bok || !vok:
				out[i].Unknown++
			case v == b:
				out[i].Same++
			case (v > b) == m.higher:
				out[i].Better++
			default:
				out[i].Worse++
			}
		}
	}
	return out, comparable, notComparable
}

// deployOutcome reads what a landing's deploy step witnessed: a deployment,
// a release that was only the merge (nothing to deploy), or a failure.
func deployOutcome(st *workflow.Step) string {
	var ev struct {
		Skipped string `json:"skipped"`
		Reason  string `json:"reason"`
	}
	unread := len(st.Evidence) > 0 && json.Unmarshal(st.Evidence, &ev) != nil
	switch {
	case st.Status != workflow.StatusPass:
		return strings.TrimSpace("failed: " + st.Status.String() + " " + ev.Reason)
	case unread:
		// Passed, but whether it deployed or found nothing to deploy is in
		// evidence that cannot be read: not claimed either way.
		return "passed; its evidence could not be read, so whether it deployed is not known"
	case ev.Skipped != "":
		return "nothing to deploy: the merge is the release"
	}
	return "deployed"
}
