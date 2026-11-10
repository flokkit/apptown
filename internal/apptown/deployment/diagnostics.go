package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Runtime diagnostics: why a deployment's app is failing, from stored host facts
// (design/subsystems/deployment.md, "Runtime diagnostics"). They bring
// together what AppTown already observes: the background observer's
// state, each part's and need's container as its Docker daemon reports it
// now, the limit events recorded for the deployment, and a crash-loop hold
// on its generation. They are about the generation the deployment settled
// on, its parts as that generation's record names them, never the
// project's newest declaration. Reading them changes nothing.

// Diagnostics is one deployment's runtime facts for its settled generation.
type Diagnostics struct {
	Reading    DetailReading `json:"reading"`
	Deployment string        `json:"deployment"`
	// Generation is the settled generation these facts are about (0:
	// nothing settled); Desired is the newest admitted, ahead of it while a
	// deploy is under way.
	Generation int64  `json:"generation"`
	Desired    int64  `json:"desiredGeneration"`
	Commit     string `json:"commit,omitempty"`
	// ReleaseNumber is the release that generation was released as, 0 when
	// none was (a landing's generation before its release, or none).
	ReleaseNumber int `json:"releaseNumber,omitempty"`
	// Runtime is the background observer's latest answer for the
	// deployment as a whole.
	Runtime DiagnosedRuntime `json:"runtime"`
	// Containers says each part's container is read from a Docker host;
	// NoContainers says its target runs none (a bucket website), so
	// container state, CPU and memory are not applicable, and each part is
	// what its address was last seen serving; otherwise Unsupported says why
	// the target gives no such facts.
	Containers   bool   `json:"containers"`
	NoContainers bool   `json:"noContainers,omitempty"`
	Unsupported  string `json:"unsupported,omitempty"`
	// Unsettled says why the parts read cannot all be named as the settled
	// generation's (a deploy is under way, or one failed part way): each
	// part then carries only the generation its own container proves.
	Unsettled string          `json:"unsettled,omitempty"`
	Parts     []PartDiagnosis `json:"parts"`
	// Events are the limit events recorded over the last day, oldest
	// first, each with the generation it happened under where known.
	Events       []LimitEvent `json:"events"`
	EventsKnown  bool         `json:"eventsKnown"`
	EventsSource string       `json:"eventsSource"`
	// Hold is AppTown's crash-loop hold on this generation, nil when
	// none was recorded.
	Hold *CrashLoopHold `json:"hold,omitempty"`
	// Logs bounds an explicit log read of one part (RecentLog).
	LogMaxLines int `json:"logMaxLines"`
	LogMaxBytes int `json:"logMaxBytes"`
}

// DiagnosedRuntime is the observer's answer: running, stopped or unknown,
// its health (healthy, unhealthy, unknown or ""), when it last answered and
// was last asked, and whether that answer is out of date (a failed refresh
// keeps the last answer with its age).
type DiagnosedRuntime struct {
	State       string    `json:"state"`
	Health      string    `json:"health,omitempty"`
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	AttemptedAt time.Time `json:"attemptedAt,omitzero"`
	Stale       bool      `json:"stale"`
	Reason      string    `json:"reason,omitempty"`
}

// diagnosisReadWithin bounds the container inspection for an explicit log read.
const diagnosisReadWithin = 5 * time.Second

// Part presences. Unknown is a read that did not answer: never stopped or
// absent. Serving and not serving are a container-less part's: its address
// answers this release's marker, or answers something else.
const (
	presenceRunning    = "running"
	presenceStopped    = "stopped"
	presenceAbsent     = "absent"
	presenceUnknown    = "unknown"
	presenceServing    = "serving"
	presenceNotServing = "not serving"
)

// PartDiagnosis is the container under one part's or need's name, as its
// daemon reported it. Generation is the generation that container is proven
// to run: the public part's by its identity labels, any other part's only
// when the deployment was settled on its recorded generation once it was
// read (a rollout replaces the other parts first). Nil is not known. When
// the latest read fails, Presence is unknown with the Reason, and Facts are
// the last ones read, with ObservedAt saying how old they are (Stale).
type PartDiagnosis struct {
	Part        string          `json:"part"`
	Public      bool            `json:"public,omitempty"`
	Need        bool            `json:"need,omitempty"`
	Generation  *int64          `json:"generation,omitempty"`
	Presence    string          `json:"presence"`
	Facts       *ContainerFacts `json:"facts,omitempty"`
	ObservedAt  time.Time       `json:"observedAt,omitzero"`
	AttemptedAt time.Time       `json:"attemptedAt,omitzero"`
	Stale       bool            `json:"stale"`
	Reason      string          `json:"reason,omitempty"`
}

// ContainerFacts are a container's process facts as its daemon keeps them.
// ExitCode is its last exit, given only when it is not running. Restarts
// is Docker's counter for this container, which a new container (a
// deploy) starts again. Health is "not configured" when the container has
// no Docker health check: not healthy.
type ContainerFacts struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Running    bool      `json:"running"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	OOMKilled  bool      `json:"oomKilled"`
	Restarts   int       `json:"restarts"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Health     string    `json:"health"`
}

// CrashLoopHold is the health controller's hold on the generation, from its
// receipt: Confirmed when the stop succeeded, otherwise only attempted.
type CrashLoopHold struct {
	Receipt   ids.ID `json:"receipt"`
	At        string `json:"at"`
	Confirmed bool   `json:"confirmed"`
	Restarts  int    `json:"restarts"`
	Window    string `json:"window"`
	ExitCode  string `json:"exitCode,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Diagnostics reads a deployment's runtime facts; the caller needs the
// deployment read grant on its project. Logs are not part of it
// (RecentLog).
func (s *Service) diagnostics(ctx context.Context, projectID ids.ID, name string, parts func(context.Context, *Deployment, *Diagnostics) error) (Diagnostics, error) {
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return Diagnostics{}, err
	}
	out := Diagnostics{Deployment: dep.Name, Generation: dep.ObservedGeneration, Desired: dep.DesiredGeneration, Commit: dep.ReleaseSHA,
		Parts: []PartDiagnosis{}, LogMaxLines: docker.LogMaxLines, LogMaxBytes: docker.LogMaxBytes}
	if out.Runtime, err = s.diagnosedRuntime(ctx, dep); err != nil {
		return Diagnostics{}, err
	}
	if dep.ObservedGeneration > 0 {
		r, err := deploymentsql.New(s.db.SQL()).ReleasedGeneration(ctx, deploymentsql.ReleasedGenerationParams{ProjectID: projectID, DeploymentID: dep.ID, Generation: dep.ObservedGeneration, State: Released.String()})
		switch {
		case err == nil:
			out.ReleaseNumber = int(r.Number)
		case !errors.Is(err, sql.ErrNoRows):
			return Diagnostics{}, err
		}
	}
	if err := parts(ctx, dep, &out); err != nil {
		return Diagnostics{}, err
	}
	out.EventsKnown, out.EventsSource = s.limitEventsSource(ctx, dep)
	if out.Events, err = s.limitEvents(ctx, dep, s.now().Add(-24*time.Hour)); err != nil {
		return Diagnostics{}, err
	}
	if out.Hold, err = s.crashLoopHold(ctx, dep); err != nil {
		return Diagnostics{}, err
	}
	return out, nil
}

// diagnosedRuntime is the observer's cached answer for the deployment; a
// deployment it has no answer for is unknown, not stopped.
func (s *Service) diagnosedRuntime(ctx context.Context, dep *Deployment) (DiagnosedRuntime, error) {
	records, err := s.listRecords(ctx, []ids.ID{dep.ProjectID})
	if err != nil {
		return DiagnosedRuntime{}, err
	}
	for _, r := range records {
		if r.deployment.ID != dep.ID {
			continue
		}
		o := s.observations.read(r, s.now())
		out := DiagnosedRuntime{State: o.State.String(), Health: o.Health, ObservedAt: o.ObservedAt, AttemptedAt: o.AttemptedAt, Stale: o.Stale}
		switch {
		case o.Err != nil:
			out.Reason = "its latest observation failed: " + safeReason(o.Err)
		case o.ObservedAt.IsZero():
			out.Reason = "not observed yet"
		case o.Stale:
			out.Reason = "its last observation is out of date"
		}
		return out, nil
	}
	return DiagnosedRuntime{State: RuntimeUnknown.String(), Stale: true, Reason: "not observed yet"}, nil
}

// diagnoseParts derives the settled parts from the host observer's one stored
// container section. Neither page reads nor detail jobs contact Docker.
func (s *Service) diagnoseParts(ctx context.Context, dep *Deployment, out *Diagnostics) error {
	applied, err := s.SettledApplied(ctx, dep)
	if err != nil {
		return err
	}
	if dep.ObservedGeneration == 0 {
		out.Unsupported = "nothing has been deployed here yet"
		return nil
	}
	var target *Target
	if !dep.TargetID.IsZero() {
		if target, err = s.Targets.ByID(ctx, dep.TargetID); err != nil {
			return err
		}
	}
	if target.runsNoContainers() {
		out.NoContainers = true
		for _, p := range diagnosedParts(applied) {
			servedPart(&p, out.Runtime, dep.ObservedGeneration)
			out.Parts = append(out.Parts, p)
		}
		return nil
	}
	if dep.Container == "" {
		_, words := s.limitEventsSource(ctx, dep)
		out.Unsupported = "its target does not report each part's container to AppTown (" + words + ")"
		for _, p := range diagnosedParts(applied) {
			p.Presence, p.Reason = presenceUnknown, "not reported by its target"
			out.Parts = append(out.Parts, p)
		}
		return nil
	}
	proj, err := s.projects.ByID(ctx, dep.ProjectID)
	if err != nil {
		return err
	}
	out.Containers = true
	containers := partContainers(dep, proj.Slug, applied)
	if target == nil {
		return errors.New("a deployment with a container has no target")
	}
	reading, err := NewHostSnapshots(s.db, s.now).reading(ctx, target)
	if err != nil {
		return err
	}
	section := reading.Containers
	out.Reading = DetailReading{ObservedAt: section.ObservedAt, AttemptedAt: reading.AttemptedAt}
	// A deployment's parts wait only for its own last act. The host-wide act
	// mark governs host-level views; a sibling's act leaves this deployment's
	// containers as last witnessed. A reading begun at the same instant as
	// the act cannot be ordered after it.
	predatesDeployment := !reading.AttemptedAt.After(dep.lastActAt())
	whyRead := ""
	switch {
	case section.Reason != "":
		whyRead = "its host's container listing could not be read: " + reading.ContainerFailure
	case section.Value == nil:
		whyRead = "waiting for the first host container reading"
	case predatesDeployment:
		whyRead = "waiting for a host container reading after the latest change"
	case s.now().Sub(section.ObservedAt) > observationStaleAfter:
		whyRead = "its host's container reading is out of date"
	}
	if whyRead != "" {
		out.Reading.Failure = whyRead
		out.Reading.Stale, out.Reading.Unknown = true, true
		out.Reading.RetryAt = reading.NextAt
	}
	byName := map[string]docker.HostContainer{}
	if section.Value != nil {
		for _, c := range *section.Value {
			byName[c.Name] = c
		}
	}
	out.Parts = diagnosedParts(applied)
	for i := range out.Parts {
		p := &out.Parts[i]
		p.AttemptedAt = reading.AttemptedAt
		c, present := byName[containers[p.Part]]
		if present && !predatesDeployment {
			health := "not configured"
			if c.State.Health != nil {
				health = c.State.Health.Status
			}
			f := &ContainerFacts{ID: c.ID, Status: c.State.Status, Running: c.State.Running,
				OOMKilled: c.State.OOMKilled, Restarts: c.RestartCount, StartedAt: c.State.StartedAt,
				FinishedAt: c.State.FinishedAt, Health: health}
			if !f.Running {
				code := c.State.ExitCode
				f.ExitCode = &code
			}
			p.Facts, p.ObservedAt = f, section.ObservedAt
			p.Presence = presenceStopped
			if f.Running {
				p.Presence = presenceRunning
			}
			if p.Public {
				p.Generation = labelledGeneration(dep, docker.Runtime{Labels: c.Config.Labels})
			}
		} else {
			p.Presence, p.ObservedAt = presenceAbsent, section.ObservedAt
		}
		if whyRead != "" {
			p.Presence, p.Reason, p.Stale = presenceUnknown, whyRead, true
		}
	}
	// Read after the containers were: a deployment still settled on its
	// recorded generation, with no attempt open, was settled while they
	// were read, so its other parts are that generation's.
	why, err := s.unsettledReason(ctx, dep)
	if err != nil {
		return err
	}
	out.Unsettled = why
	for i := range out.Parts {
		p := &out.Parts[i]
		if p.Presence == presenceUnknown {
			continue
		}
		if why == "" && !p.Public {
			g := dep.ObservedGeneration
			p.Generation = &g
		}

	}
	return nil
}

// labelledGeneration is the generation a container's identity labels name
// when they name this deployment, nil otherwise.
func labelledGeneration(dep *Deployment, rt docker.Runtime) *int64 {
	carried := rt.Labels
	if carried[dockerlabel.Deployment] != string(dep.label()) {
		return nil
	}
	g, err := strconv.ParseInt(carried[dockerlabel.Generation], 10, 64)
	if err != nil || g <= 0 {
		return nil
	}
	return &g
}

// diagnosedParts is the settled generation's parts and needs by name, the
// public part first; a generation that recorded none is its public part
// alone, named "".
func diagnosedParts(applied AppliedProfile) []PartDiagnosis {
	if len(applied.Parts) == 0 {
		return []PartDiagnosis{{Public: true}}
	}
	var out []PartDiagnosis
	for _, n := range slices.Sorted(maps.Keys(applied.Parts)) {
		ap := applied.Parts[n]
		out = append(out, PartDiagnosis{Part: n, Public: ap.Public, Need: ap.Need})
	}
	for _, n := range slices.Sorted(maps.Keys(applied.Services)) {
		if _, ok := applied.Parts[n]; !ok {
			out = append(out, PartDiagnosis{Part: n, Need: true})
		}
	}
	slices.SortStableFunc(out, func(a, b PartDiagnosis) int {
		switch {
		case a.Public == b.Public:
			return 0
		case a.Public:
			return -1
		}
		return 1
	})
	return out
}

// crashLoopHold is the newest crash-loop hold receipt for the deployment's
// settled generation, nil when there is none.
func (s *Service) crashLoopHold(ctx context.Context, dep *Deployment) (*CrashLoopHold, error) {
	if dep.ObservedGeneration == 0 {
		return nil, nil
	}
	lines, err := audit.Recent(ctx, s.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.crash-loop-held", Subject: dep.Name, Limit: 1, PayloadContains: map[string]any{"deploymentId": dep.ID, "generation": dep.ObservedGeneration}})
	if err != nil {
		return nil, fmt.Errorf("reading crash-loop holds: %w", err)
	}
	for _, l := range lines {
		var p struct {
			DeploymentID ids.ID `json:"deploymentId"`
			Generation   int64  `json:"generation"`
			OK           bool   `json:"ok"`
			Restarts     int    `json:"restarts"`
			Window       string `json:"window"`
			ExitCode     string `json:"exitCode"`
			Detail       string `json:"detail"`
		}
		if json.Unmarshal(l.Payload, &p) != nil || p.DeploymentID != dep.ID || p.Generation != dep.ObservedGeneration {
			continue
		}
		return &CrashLoopHold{Receipt: l.ID, At: l.At, Confirmed: p.OK, Restarts: p.Restarts, Window: p.Window, ExitCode: p.ExitCode, Detail: p.Detail}, nil
	}
	return nil, nil
}

// PartLog is one explicit read of a part's recent log, with its bounds and
// when it was read. It is shown to the person who asked and kept nowhere.
// Container is the container read; Generation is the generation it is
// proven to run (PartDiagnosis.Generation's rule), nil when not known.
type PartLog struct {
	Part       string    `json:"part"`
	Container  string    `json:"container"`
	Generation *int64    `json:"generation,omitempty"`
	Text       string    `json:"text"`
	Truncated  bool      `json:"truncated"`
	MaxLines   int       `json:"maxLines"`
	MaxBytes   int       `json:"maxBytes"`
	ReadAt     time.Time `json:"readAt"`
}

// RecentLog reads a part's or need's recent log on the deployment's own
// host. A log can hold the app's secrets, so it takes the grant to operate
// the deployment, not to read it. The part is one the settled generation
// recorded, and its container is the one AppTown names for it there: no
// caller names a container.
func (s *Service) RecentLog(ctx context.Context, projectID ids.ID, name, part string) (PartLog, error) {
	if s.authority == nil {
		return PartLog{}, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentOperate, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.logs"}}); err != nil {
		return PartLog{}, err
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return PartLog{}, err
	}
	if dep.ObservedGeneration == 0 {
		return PartLog{}, fault.New(fault.Conflict, "nothing has been deployed here yet")
	}
	if dep.Container == "" {
		return PartLog{}, fault.New(fault.Unprocessable, "its target does not give AppTown container logs")
	}
	applied, err := s.SettledApplied(ctx, dep)
	if err != nil {
		return PartLog{}, err
	}
	proj, err := s.projects.ByID(ctx, dep.ProjectID)
	if err != nil {
		return PartLog{}, err
	}
	container, ok := partContainers(dep, proj.Slug, applied)[part]
	if !ok {
		return PartLog{}, fault.New(fault.NotFound, fmt.Sprintf("generation %d of %s has no part or need %q", dep.ObservedGeneration, dep.Name, part))
	}
	dk, err := s.hosts.DockerScopeFor(ctx, dep.TargetID)
	if err != nil {
		return PartLog{}, fault.New(fault.Unavailable, "its host cannot be asked: "+safeReason(err))
	}
	// The log read is of the container inspected, by its ID, so a
	// replacement under the same name meanwhile cannot change whose it is.
	inspectCtx, cancel := context.WithTimeout(ctx, diagnosisReadWithin)
	rt, err := docker.Inspect(inspectCtx, dk, container)
	cancel()
	switch {
	case errors.Is(err, docker.ErrContainerAbsent):
		return PartLog{}, fault.New(fault.NotFound, "its container is gone")
	case err != nil:
		return PartLog{}, fault.New(fault.Unavailable, "its container could not be read: "+safeReason(err))
	case rt.ID == "":
		return PartLog{}, fault.New(fault.Unavailable, "its host did not name the container")
	}
	got, err := docker.ReadRecentLog(ctx, dk, rt.ID)
	switch {
	case errors.Is(err, docker.ErrContainerAbsent):
		return PartLog{}, fault.New(fault.NotFound, "its container is gone")
	case err != nil:
		return PartLog{}, fault.New(fault.Unavailable, safeReason(err))
	}
	out := PartLog{Part: part, Container: rt.ID, Text: got.Text, Truncated: got.Truncated,
		MaxLines: docker.LogMaxLines, MaxBytes: docker.LogMaxBytes, ReadAt: s.now()}
	if applied.Parts[part].Public || len(applied.Parts) == 0 {
		out.Generation = labelledGeneration(dep, rt)
	} else if why, err := s.unsettledReason(ctx, dep); err != nil {
		return PartLog{}, err
	} else if why == "" {
		g := dep.ObservedGeneration
		out.Generation = &g
	}
	return out, nil
}

// safeReason is a failure in words a person may be shown: a timeout or
// cancellation as such, AppTown's own refusals as they are, and anything
// else a host or provider said as a fixed phrase, since its text may name
// an address or a credential.
func safeReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the host reading timed out"
	case errors.Is(err, context.Canceled):
		return "the read was cancelled"
	case errors.Is(err, docker.ErrLogUnreadable):
		return err.Error()
	case fault.KindOf(err) != fault.Internal:
		return fault.Message(err)
	}
	return "the host did not answer"
}

// Diagnostics reads the last background observation for the current deployment settings.
func (s *Service) Diagnostics(ctx context.Context, projectID ids.ID, name string) (Diagnostics, error) {
	if s.authority == nil {
		return Diagnostics{}, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.diagnostics"}}); err != nil {
		return Diagnostics{}, err
	}
	return s.collectDiagnostics(ctx, projectID, name)
}

func (s *Service) collectDiagnostics(ctx context.Context, projectID ids.ID, name string) (Diagnostics, error) {
	return s.diagnostics(ctx, projectID, name, s.diagnoseParts)
}

// servedPart is a container-less part as the observer last saw its
// address: serving this release when the address answered this release's
// marker, not serving when it answered something else, and unknown when
// it did not answer or the answer is out of date. The marker names the
// settled generation's publication, so serving proves that generation.
func servedPart(p *PartDiagnosis, rt DiagnosedRuntime, generation int64) {
	p.ObservedAt, p.AttemptedAt, p.Stale = rt.ObservedAt, rt.AttemptedAt, rt.Stale
	switch {
	case rt.Stale || rt.State != RuntimeRunning.String():
		p.Presence, p.Reason = presenceUnknown, rt.Reason
		if p.Reason == "" {
			p.Reason = "AppTown has no current answer from its address"
		}
	case rt.Health == "healthy":
		p.Presence = presenceServing
		g := generation
		p.Generation = &g
	case rt.Health == "unhealthy":
		p.Presence, p.Reason = presenceNotServing, "its address answers, but not with this release"
	default:
		p.Presence, p.Reason = presenceUnknown, "whether its address serves this release is not known"
	}
}
