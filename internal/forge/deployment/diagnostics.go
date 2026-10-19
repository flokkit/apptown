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
	"sync"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/dockerlabel"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/ids"
)

// Runtime diagnostics: why a deployment's app is failing, read on request
// (design/subsystems/deployment.md, "Runtime diagnostics"). They bring
// together what the forge already observes: the background observer's
// state, each part's and need's container as its Docker daemon reports it
// now, the limit events recorded for the deployment, and a crash-loop hold
// on its generation. They are about the generation the deployment settled
// on, its parts as that generation's record names them, never the
// project's newest declaration. Reading them changes nothing.

// Diagnostics is one deployment's runtime facts for its settled generation.
type Diagnostics struct {
	Deployment string `json:"deployment"`
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
	// otherwise Unsupported says why the target gives no such facts.
	Containers  bool   `json:"containers"`
	Unsupported string `json:"unsupported,omitempty"`
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
	// Hold is the forge's crash-loop hold on this generation, nil when
	// none was recorded.
	Hold *CrashLoopHold `json:"hold,omitempty"`
	// Logs bounds an explicit log read of one part (RecentLog).
	LogMaxLines int `json:"logMaxLines"`
	LogMaxBytes int `json:"logMaxBytes"`
}

// DiagnosedRuntime is the observer's answer: running, stopped or unknown,
// when it last answered and was last asked, and whether that answer is out
// of date (a failed refresh keeps the last answer with its age).
type DiagnosedRuntime struct {
	State       string    `json:"state"`
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	AttemptedAt time.Time `json:"attemptedAt,omitzero"`
	Stale       bool      `json:"stale"`
	Reason      string    `json:"reason,omitempty"`
}

// diagnosisReadWithin bounds each part's read, as the observer bounds its
// provider calls, so one silent host keeps a page waiting seconds, not
// minutes.
const diagnosisReadWithin = 5 * time.Second

// Part presences. Unknown is a read that did not answer: never stopped or
// absent.
const (
	PresenceRunning = "running"
	PresenceStopped = "stopped"
	PresenceAbsent  = "absent"
	PresenceUnknown = "unknown"
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

// diagnosed keeps each deployment's last part facts, so a failed read can
// show them with their age. An entry is for one settled generation and its
// container names; any other generation finds nothing. A container replaced
// under the same name is seen only when a read answers (its ID changes).
type diagnosed struct {
	mu      sync.Mutex
	entries map[ids.ID]diagnosedEntry
}

type diagnosedEntry struct {
	key   string
	parts map[string]PartDiagnosis
}

// diagnosisKey names a settled generation's container names: settling on
// another generation makes the old facts another key's.
func diagnosisKey(generation int64, containers map[string]string) string {
	key := strconv.FormatInt(generation, 10)
	for _, part := range slices.Sorted(maps.Keys(containers)) {
		key += "\x00" + part + "=" + containers[part]
	}
	return key
}

func (d *diagnosed) last(id ids.ID, key, part string) (PartDiagnosis, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[id]
	if !ok || e.key != key {
		return PartDiagnosis{}, false
	}
	p, ok := e.parts[part]
	return p, ok
}

func (d *diagnosed) keep(id ids.ID, key string, p PartDiagnosis) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = map[ids.ID]diagnosedEntry{}
	}
	e, ok := d.entries[id]
	if !ok || e.key != key {
		e = diagnosedEntry{key: key, parts: map[string]PartDiagnosis{}}
		d.entries[id] = e
	}
	e.parts[p.Part] = p
}

// Diagnostics reads a deployment's runtime facts; the caller needs the
// deployment read grant on its project. Logs are not part of it
// (RecentLog).
func (s *Service) Diagnostics(ctx context.Context, projectID ids.ID, name string) (Diagnostics, error) {
	if s.authority == nil {
		return Diagnostics{}, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.diagnostics"}}); err != nil {
		return Diagnostics{}, err
	}
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
	if err := s.diagnoseParts(ctx, dep, &out); err != nil {
		return Diagnostics{}, err
	}
	out.EventsKnown, out.EventsSource = s.LimitEventsSource(ctx, dep)
	if out.Events, err = s.LimitEvents(ctx, dep, s.now().Add(-24*time.Hour)); err != nil {
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
		out := DiagnosedRuntime{State: o.State.String(), ObservedAt: o.ObservedAt, AttemptedAt: o.AttemptedAt, Stale: o.Stale}
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

// diagnoseParts reads each part's and need's container of the settled
// generation from the deployment's own Docker host.
func (s *Service) diagnoseParts(ctx context.Context, dep *Deployment, out *Diagnostics) error {
	applied, err := s.SettledApplied(ctx, dep)
	if err != nil {
		return err
	}
	if dep.ObservedGeneration == 0 {
		out.Unsupported = "nothing has been deployed here yet"
		return nil
	}
	if dep.Container == "" {
		_, words := s.LimitEventsSource(ctx, dep)
		out.Unsupported = "its target does not report each part's container to AppTown (" + words + ")"
		for _, p := range diagnosedParts(applied) {
			p.Presence, p.Reason = PresenceUnknown, "not reported by its target"
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
	key := diagnosisKey(dep.ObservedGeneration, containers)
	dk, hostErr := s.hosts.DockerScopeFor(ctx, dep.TargetID)
	read := map[string]bool{}
	for _, p := range diagnosedParts(applied) {
		p.AttemptedAt = s.now()
		var why string
		if hostErr != nil {
			why = "its host cannot be asked: " + safeReason(hostErr)
		} else {
			why = s.readPart(ctx, dk, dep, containers[p.Part], &p)
		}
		if why == "" {
			read[p.Part] = true
		} else {
			// The read did not answer: unknown now, with the last facts
			// read and their age.
			p.Presence, p.Reason, p.Stale = PresenceUnknown, why, true
			if last, ok := s.diagnosed.last(dep.ID, key, p.Part); ok {
				p.Facts, p.ObservedAt, p.Generation = last.Facts, last.ObservedAt, last.Generation
			}
		}
		out.Parts = append(out.Parts, p)
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
		if !read[p.Part] {
			continue
		}
		if why == "" && !p.Public {
			g := dep.ObservedGeneration
			p.Generation = &g
		}
		s.diagnosed.keep(dep.ID, key, *p)
	}
	return nil
}

// readPart asks the daemon for the container under one part's name and
// fills p, or says why it could not. The public part's container names its
// generation; no other part's does (labelledGeneration).
func (s *Service) readPart(ctx context.Context, dk docker.Scope, dep *Deployment, container string, p *PartDiagnosis) string {
	ctx, cancel := context.WithTimeout(ctx, diagnosisReadWithin)
	defer cancel()
	rt, err := docker.Inspect(ctx, dk, container)
	switch {
	case errors.Is(err, docker.ErrContainerAbsent):
		p.Presence, p.ObservedAt = PresenceAbsent, s.now()
		return ""
	case err != nil:
		return "its container could not be read: " + safeReason(err)
	case rt.State == nil:
		return "its host did not report the container's state"
	}
	if p.Public {
		p.Generation = labelledGeneration(dep, rt)
	}
	f := ContainerFacts{ID: rt.ID, Status: rt.State.Status, Running: rt.Running, OOMKilled: rt.State.OOMKilled, Restarts: rt.State.RestartCount,
		StartedAt: rt.State.StartedAt, FinishedAt: rt.State.FinishedAt, Health: rt.State.Health}
	if !rt.Running {
		code := rt.State.ExitCode
		f.ExitCode = &code
	}
	if f.Health == "" {
		f.Health = "not configured"
	}
	p.Presence, p.Facts, p.ObservedAt = PresenceStopped, &f, s.now()
	if rt.Running {
		p.Presence = PresenceRunning
	}
	return ""
}

// labelledGeneration is the generation a container's identity labels name
// when they name this deployment, nil otherwise.
func labelledGeneration(dep *Deployment, rt docker.Runtime) *int64 {
	if rt.Labels[dockerlabel.Deployment] != string(dep.Label()) {
		return nil
	}
	g, err := strconv.ParseInt(rt.Labels[dockerlabel.Generation], 10, 64)
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
	lines, err := audit.Recent(ctx, s.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.crash-loop-held", Subject: dep.Name, Limit: 20})
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
// recorded, and its container is the one the forge names for it there: no
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
// cancellation as such, the forge's own refusals as they are, and anything
// else a host or provider said as a fixed phrase, since its text may name
// an address or a credential.
func safeReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "it did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the read was cancelled"
	case errors.Is(err, docker.ErrLogUnreadable):
		return err.Error()
	case fault.KindOf(err) != fault.Internal:
		return fault.Message(err)
	}
	return "the host did not answer"
}
