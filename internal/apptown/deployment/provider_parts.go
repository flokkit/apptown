package deployment

import (
	"context"
	"slices"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// Parts a provider reports (deploy.Observation.Parts): a target with no
// Docker daemon AppTown can ask, whose observation names what runs each
// part and need (a Fly Machine). The observation sweep carries them, the
// detail reader asks the driver for their use (deploy.UsageReader), and the
// limit-event sweep records their exits. The page reads the answers as it
// reads a Docker host's: what was not answered is unknown, never zero.

// reportsParts says the target's observations report each part.
func (t *Target) reportsParts() bool { return t != nil && kindRuntime[t.Kind].reportsParts }

// observedParts is the observer's latest answer for dep, and why its parts
// are not known now ("" when they are).
func (s *Service) observedParts(ctx context.Context, dep *Deployment) (RuntimeObservation, string, error) {
	records, err := s.listRecords(ctx, []ids.ID{dep.ProjectID})
	if err != nil {
		return RuntimeObservation{}, "", err
	}
	for _, r := range records {
		if r.deployment.ID != dep.ID {
			continue
		}
		o := s.observations.read(r, s.now())
		switch {
		case o.Err != nil:
			return o, "its latest observation failed: " + safeReason(o.Err), nil
		case o.ObservedAt.IsZero():
			return o, "not observed yet", nil
		case o.Stale:
			return o, "its last observation is out of date", nil
		}
		return o, "", nil
	}
	return RuntimeObservation{}, "not observed yet", nil
}

// diagnoseReportedParts fills out's parts from the observation's: what runs
// each, its state, when it started and how it last exited.
func (s *Service) diagnoseReportedParts(ctx context.Context, dep *Deployment, applied AppliedProfile, out *Diagnostics) error {
	o, why, err := s.observedParts(ctx, dep)
	if err != nil {
		return err
	}
	out.Reading = DetailReading{ObservedAt: o.ObservedAt, AttemptedAt: o.AttemptedAt}
	if why != "" {
		out.Reading.Failure, out.Reading.Stale, out.Reading.Unknown = why, true, true
	}
	reported, unnamed := reportedOf(o.parts, dep.ObservedGeneration)
	out.Parts = diagnosedParts(applied)
	for i := range out.Parts {
		p := &out.Parts[i]
		p.AttemptedAt = o.AttemptedAt
		m, present := reported[p.Part]
		switch {
		case why != "":
			p.Presence, p.Reason, p.Stale = presenceUnknown, why, true
		case !present && unnamed:
			p.Presence, p.Reason = presenceUnknown, unnamedReason
		case !present:
			p.Presence, p.ObservedAt = presenceAbsent, o.ObservedAt
		case m.Unreachable:
			p.Presence, p.Reason = presenceUnknown, "its provider cannot reach the host it runs on"
		default:
			p.Facts, p.ObservedAt = reportedFacts(m), o.ObservedAt
			switch {
			case m.Running:
				p.Presence = presenceRunning
			case stoppedState(m):
				p.Presence = presenceStopped
			default:
				p.Presence, p.Reason = presenceUnknown, "it is "+m.Status
			}
			if m.Generation > 0 {
				g := m.Generation
				p.Generation = &g
			}
		}
	}
	return s.settledParts(ctx, dep, out)
}

// reportedOf is the reported parts that run generation g, by name (a need
// serves every generation), and whether the provider listed one it could
// not name, which may be any part it did not list.
func reportedOf(parts []deploy.PartRuntime, g int64) (map[string]deploy.PartRuntime, bool) {
	out, unnamed := map[string]deploy.PartRuntime{}, false
	for _, p := range parts {
		switch {
		case p.Part == "":
			unnamed = true
		case p.Generation == 0 || p.Generation == g:
			out[p.Part] = p
		}
	}
	return out, unnamed
}

const unnamedReason = "its provider cannot reach a host that may run it"

// stoppedState says a part that is not running is stopped, using nothing:
// any other state (starting, replacing) is on its way somewhere and is
// not known.
func stoppedState(m deploy.PartRuntime) bool {
	return !m.Running && slices.Contains([]string{"stopped", "suspended", "failed", "destroyed"}, m.Status)
}

// reportedFacts are a reported part's facts in a container's terms: its
// last exit only when it is not running.
func reportedFacts(m deploy.PartRuntime) *ContainerFacts {
	f := &ContainerFacts{ID: m.ID, Status: m.Status, Running: m.Running, StartedAt: m.StartedAt, Health: m.Health}
	if f.Health == "" {
		f.Health = "not configured"
	}
	if last, ok := lastExit(m.Exits); ok && !m.Running {
		code := last.Code
		f.ExitCode, f.OOMKilled, f.FinishedAt = &code, last.OOM, last.At
	}
	return f
}

func lastExit(exits []deploy.PartExit) (deploy.PartExit, bool) {
	if len(exits) == 0 {
		return deploy.PartExit{}, false
	}
	return slices.MaxFunc(exits, func(a, b deploy.PartExit) int { return a.At.Compare(b.At) }), true
}

// reportedUse is each part's use as the deployment's driver reads it now.
func (s *Service) reportedUse(ctx context.Context, t *Target, d *Deployment, parts []string, applied AppliedProfile, unknown func(string) []PartMemory) ([]PartMemory, error) {
	if len(parts) == 0 {
		return unknown("its settled generation recorded no parts"), nil
	}
	driver, err := s.providers.Driver(ctx, t, d)
	if err != nil {
		return unknown("its target cannot be asked: " + safeReason(err)), nil
	}
	reader, ok := driver.(deploy.UsageReader)
	if !ok {
		return unknown("its target does not report memory use to AppTown"), nil
	}
	got, err := reader.ReadUsage(ctx, d.Placement())
	if err != nil {
		return unknown("its target did not answer: " + safeReason(err)), nil
	}
	reported, unnamed := reportedOf(got, d.ObservedGeneration)
	out := make([]PartMemory, 0, len(parts))
	for _, p := range parts {
		m, present := reported[p]
		pm := PartMemory{Part: p, Need: applied.Parts[p].Need}
		switch {
		case !present && unnamed:
			pm.Reason = unnamedReason
		case !present:
			pm.Reason = "its target lists nothing running it"
		case m.Unreachable:
			pm.Reason = "its provider cannot reach the host it runs on"
		case !m.Running && !stoppedState(m):
			pm.Reason = "it is " + m.Status
		case !m.Running:
			// What is there and stopped uses nothing now.
			pm.LimitBytes, pm.CPULimit, pm.CPUKind, pm.Image, pm.Known, pm.Stopped = m.MemoryLimit, m.CPULimit, m.CPUKind, m.Image, true, true
		case m.Use == nil:
			pm.Reason = m.UseReason
		default:
			pm.UsedBytes, pm.CPUCores, pm.LimitBytes, pm.CPULimit, pm.CPUKind, pm.Image, pm.Known = m.Use.MemoryBytes, m.Use.CPUCores, m.MemoryLimit, m.CPULimit, m.CPUKind, m.Image, true
		}
		out = append(out, pm)
	}
	return out, nil
}

// recordReportedExits records, from each standing deployment's latest
// observation, its parts' out-of-memory kills and the exits nobody asked
// for. The provider keeps only the newest few events of each, and the
// observation sees only the Machines listed when it looked (a failed
// release's are removed), so the sweep records what it sees each time;
// one already recorded is not recorded again (the table's key).
func (s *Service) recordReportedExits(ctx context.Context, deps []ProjectDeployment) error {
	since := s.now().Add(-limitEventKeep)
	for _, d := range deps {
		// Every generation's: a release's new Machines are where a new
		// image that needs more memory is killed.
		for _, m := range s.observations.parts(d.ID) {
			for _, ex := range m.Exits {
				if ex.Requested || ex.At.Before(since) {
					continue
				}
				e := LimitEvent{Part: m.Part, Kind: LimitOutOfMemory, Container: m.ID, At: ex.At}
				if !ex.OOM {
					code := ex.Code
					e.Kind, e.ExitCode = LimitExited, &code
				}
				if m.Generation > 0 {
					g := m.Generation
					e.Generation = &g
				}
				if err := s.recordLimitEvent(ctx, d.ID, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// parts is the parts the latest observation of a deployment reported, as
// observed, whatever its age: an exit a provider reported stays a fact.
func (o *observations) parts(id ids.ID) []deploy.PartRuntime {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.entries[id].value.parts
}
