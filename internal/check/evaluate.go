package check

import (
	"context"
	"strings"
)

// Evaluate judges every declaration against one source, in one mode.
//
// sh: one container run per check against the staged tree. AI rubrics are
// judged by AppTown against a committed checkout. runtime: probe the source's
// app through both doors. tool: the
// scanner the check names, from the catalogue the caller gave the judge.
//
// A capability the judge or the source lacks is cannot-assess with the reason,
// never an error: the tree is not at fault for what the judge could not do.
func (j *Judge) Evaluate(ctx context.Context, checks []Declared, src Source, mode Mode) ([]Evaluation, error) {
	var out []Evaluation
	for _, d := range checks {
		switch Evaluator(d.Declaration) {
		case "sh":
			if j.Run == nil {
				out = append(out, cannot(d, mode, "no way to run a command here"))
				continue
			}
			out = append(out, j.evaluateSh(d, src, mode, j.namePrefix()))
		case "runtime":
			if na := NotApplicableHere(d, src, mode); na != nil {
				out = append(out, *na)
				continue
			}
			out = append(out, j.evaluateRuntime(ctx, d, src, mode))
		case "tool":
			out = append(out, j.EvaluateTool(d, src, mode, j.namePrefix()))
		}
	}
	var rubrics []Declared
	for _, d := range checks {
		if Evaluator(d.Declaration) != "llm" {
			continue
		}
		if na := NotApplicableHere(d, src, mode); na != nil {
			out = append(out, *na)
			continue
		}
		rubrics = append(rubrics, d)
	}
	out = append(out, j.evaluateRubrics(ctx, rubrics, src, mode)...)
	return out, nil
}

func (j *Judge) namePrefix() string {
	if j.prefix == "" {
		return "check"
	}
	return j.prefix
}

// evaluateRubrics invokes each rubric independently.
func (j *Judge) evaluateRubrics(ctx context.Context, checks []Declared, src Source, mode Mode) []Evaluation {
	var out []Evaluation
	for _, d := range checks {
		out = append(out, cannot(d, mode, "AI rubrics are judged by AppTown against a committed checkout"))
	}
	return out
}

// evaluateRuntime probes the source's booted app through both doors.
func (j *Judge) evaluateRuntime(ctx context.Context, d Declared, src Source, mode Mode) Evaluation {
	ev := Evaluation{Declared: d, Mode: mode}
	ps, err := ParseProbe(d.Probe)
	if err != nil {
		ev.Outcome, ev.Detail = Error, err.Error()
		return ev
	}
	if ps.MemoryDeclared || ps.Standing() {
		// The contract a part runs under and what AppTown applied to a
		// deployment are AppTown's records, not this judge's.
		ev.Outcome, ev.Detail, ev.Fault = CannotAssess, "judged by AppTown, against the contract and the deployment it runs", true
		return ev
	}
	if j.Probe == nil || strings.TrimSpace(src.App) == "" {
		ev.Outcome, ev.Detail, ev.Fault = CannotAssess, "no booted app to probe", true
		return ev
	}
	if ps.BrowserEnv {
		// Judged only where AppTown knows what the page is given.
		ev.Outcome, ev.Detail, ev.Fault = CannotAssess, "judging what a page is given needs AppTown's integration settings", true
		return ev
	}
	if ps.Readiness {
		path, declared := "", false
		if j.readiness != nil {
			path, declared = j.readiness(src)
		}
		if !declared {
			ev.Outcome, ev.Detail = DoesNotConform, "nothing declares when this app is ready"
			ev.Findings = []Finding{conformanceFinding(d, ev.Detail, "")}
			return ev
		}
		ps.Path = path
	}
	naked := src.Naked
	if naked == "" {
		naked = src.App
	}
	state, detail := ProbeBothDoors(ctx, j.Probe, src.App, naked, ps)
	ev.Outcome, ev.Detail = state, detail
	if state == DoesNotConform {
		ev.Findings = []Finding{conformanceFinding(d, detail, "")}
	}
	return ev
}

func cannot(d Declared, mode Mode, why string) Evaluation {
	return Evaluation{Declared: d, Mode: mode, Outcome: CannotAssess, Detail: why}
}
