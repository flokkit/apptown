// Package briefs writes advisory prose from workflow evidence. One shared
// Service serializes cache reads, model work and completed writes per key.
// Prose never changes a check result or grants permission to act.
package briefs

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
	"golang.org/x/sync/singleflight"
)

type selection interface {
	Resolve(context.Context, ids.ID, string) (string, string, string, error)
}
type evidence interface {
	Step(context.Context, ids.ID) (workflow.StepContext, error)
	ApprovalBrief(context.Context, ids.ID) (workflow.ApprovalBriefInputs, error)
	FailureAdvice(context.Context, ids.ID) (workflow.ApprovalBriefInputs, error)
	SaveBrief(context.Context, ids.ID, string, string, string) error
	BriefDiff(context.Context, workflow.ApprovalBriefInputs) string
}
type Service struct {
	now        func() time.Time
	work       evidence
	selection  selection
	completion modelcall.Completer
	flights    singleflight.Group
	failures   sync.Map // cache name -> cooling deadline; no persistence
}

func New(work *workflow.Service, models selection, completion modelcall.Completer, now func() time.Time) *Service {
	return &Service{work: work, selection: models, completion: completion, now: now}
}
func (s *Service) Approval(ctx context.Context, id ids.ID) (string, error) {
	if _, err := s.work.Step(ctx, id); err != nil {
		return "", err
	}
	v, err, _ := s.flights.Do("approval/"+id.String(), func() (any, error) {
		in, err := s.work.ApprovalBrief(ctx, id)
		if err != nil {
			return "", err
		}
		if in.Cached != "" {
			return in.Cached, nil
		}
		text, err := s.buildApprovalBrief(ctx, in, s.work.BriefDiff(ctx, in))
		if err != nil {
			return "", err
		}
		if text != "" {
			s.save(ctx, in, "approval-brief", in.CacheName, text)
		}
		return text, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}
func (s *Service) save(ctx context.Context, in workflow.ApprovalBriefInputs, kind, name, body string) {
	if err := s.work.SaveBrief(context.WithoutCancel(ctx), in.Run.ID, kind, name, body); err != nil {
		log.Printf("saving %s on run %s: %v", kind, in.Run.ID, err)
	}
}

// Advice is guidance for a person; Recommend is empty unless a current act offers it.
type Advice struct {
	Text      string `json:"text"`
	Recommend string `json:"recommend"`
}

func (s *Service) Failure(ctx context.Context, id ids.ID) (Advice, error) {
	// Each reader checks scope and the current target, including readers joining a flight.
	current, err := s.work.FailureAdvice(ctx, id)
	if err != nil {
		return Advice{}, err
	}
	name := current.CacheName
	v, err, _ := s.flights.Do("failure/"+name, func() (any, error) {
		in, err := s.work.FailureAdvice(ctx, id)
		if err != nil {
			return Advice{}, err
		}
		if in.CacheName != name {
			return Advice{}, nil
		}
		if in.Cached != "" {
			var a Advice
			if err := json.Unmarshal([]byte(in.Cached), &a); err != nil {
				return Advice{}, err
			}
			return a, nil
		}
		if until, ok := s.failures.Load(name); ok && clock.Or(s.now)().Before(until.(time.Time)) {
			return Advice{}, nil
		}
		a, err := s.buildFailure(ctx, in)
		if err != nil {
			return Advice{}, err
		}
		if a.Text != "" {
			raw, err := json.Marshal(a)
			if err != nil {
				return Advice{}, err
			}
			s.save(ctx, in, "failure-advice", name, string(raw))
		}
		return a, nil
	})
	if err != nil {
		return Advice{}, err
	}
	a := v.(Advice)
	// Re-read after the call: the offered acts can change while the model runs.
	current, err = s.work.FailureAdvice(ctx, id)
	if err != nil {
		return Advice{}, err
	}
	if current.CacheName != name {
		return Advice{}, nil
	}
	a.Recommend = offered(a.Recommend, current.Acts)
	return a, nil
}
func offered(act string, acts []workflow.NextAct) string {
	switch act {
	case "send-back", "rework", "override", "retry":
	default:
		return ""
	}
	for _, a := range acts {
		if string(a.Act) == act {
			return act
		}
	}
	return ""
}
func parseAdvice(raw string, acts []workflow.NextAct) Advice {
	raw = strings.TrimSpace(raw)
	first, rest, ok := strings.Cut(raw, "\n")
	// Malformed prose is still an answer. Only a valid act line is metadata.
	switch strings.TrimSpace(first) {
	case "send-back", "rework", "override", "retry":
		if ok && strings.TrimSpace(rest) != "" {
			return Advice{Text: strings.TrimSpace(rest), Recommend: offered(strings.TrimSpace(first), acts)}
		}
	}
	return Advice{Text: raw}
}
func (s *Service) buildFailure(ctx context.Context, in workflow.ApprovalBriefInputs) (Advice, error) {
	pattern, _, _, err := s.selection.Resolve(ctx, in.Run.ProjectID, "main-strong")
	if err != nil {
		s.coolFailure(in.CacheName)
		return Advice{}, err
	}
	if pattern == "" || s.completion == nil {
		return Advice{}, nil
	}
	material, err := json.Marshal(struct {
		Ask    *workflow.Change
		Asked  *workflow.PersonFeedback
		Failed *workflow.Step
		Others []*workflow.Step
		Acts   []workflow.NextAct
		Diff   string
	}{in.Change, in.Asked, in.Step, in.Steps, in.Acts, s.work.BriefDiff(ctx, in)})
	if err != nil {
		return Advice{}, err
	}
	out, _, err := s.completion.Ask(ctx, modelcall.Call{Purpose: "failure-advice", ProjectID: in.Run.ProjectID, RunID: in.Run.ID, Model: pattern, System: failurePrompt, User: string(material)})
	if err != nil {
		s.coolFailure(in.CacheName)
		return Advice{}, nil
	}
	return parseAdvice(out, in.Acts), nil
}

const failurePrompt = `Explain this held check to the person deciding what to do. The first line must be exactly one offered act: send-back, rework, override or retry. Then write two or three plain sentences explaining what went wrong and what should happen. No labelled fields or headings. For example: The reviewer found a real bug: on HTTPS, WordPress builds its links with the wrong scheme. So the agent should fix it, not you accept it anyway.
Use only recorded facts. A report is a claim. Overrides are exceptions, not passes.
Acts: send-back asks the agent to fix the change with the person's note; rework gives the stage agent another round; override accepts a failed check as an exception with a reason; retry runs the stopped check again. Choose only an act offered for this held check. The person decides; advice does not change the result.`

// Expired entries are discarded when recording failures, so old attempts do not
// accumulate throughout an outage.
func (s *Service) coolFailure(name string) {
	now := clock.Or(s.now)()
	s.failures.Range(func(key, value any) bool {
		if !now.Before(value.(time.Time)) {
			s.failures.Delete(key)
		}
		return true
	})
	s.failures.Store(name, now.Add(10*time.Minute))
}
