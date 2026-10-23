package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// Limit events: what happened when a standing deployment's container hit
// a limit, as its Docker daemon reported it. The health controller's
// sweep reads each Docker host's event log once (RecordLimitEvents); the
// deployment's page and the memory checks read them back (LimitEvents,
// LimitSummary). They are observations, not acts: no receipt, kept 30
// days.

// Limit event kinds.
const (
	LimitOutOfMemory = "out-of-memory" // the kernel killed it at its memory limit
	LimitExited      = "exited"        // it exited, not stopped by anyone: an observation, not proof a limit was hit
)

// A crash-loop hold is AppTown's own act, with its receipt
// (deployment.crash-loop-held, saying whether the stop worked); it is not
// a limit event, which is only what a daemon reported.

const (
	// limitEventReach is how far back a read reaches when nothing newer
	// is recorded for the host: Docker replays only its recent events.
	limitEventReach = time.Hour
	// limitEventKeep is how long an event is kept.
	limitEventKeep = 30 * 24 * time.Hour
	// stopPairing is how close a kill or an out-of-memory kill comes to a
	// container's exit for the exit to be theirs, not its own: an exit
	// after AppTown or Docker stopped it is not the app exiting.
	stopPairing = 10 * time.Second
)

// LimitEvent is one recorded event.
type LimitEvent struct {
	Part      string    `json:"part"`
	Kind      string    `json:"kind"`
	Container string    `json:"container"`
	At        time.Time `json:"at"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	// Generation is the deployment generation it happened under: the one
	// its container's identity labels name, else the one the deployment
	// had settled on by then, when it was still settled with no deploy
	// under way as the event was recorded. Nil is not known (an unlabelled
	// container's event during or after an unsettled deploy, or one from
	// before AppTown kept this).
	Generation *int64 `json:"generation,omitempty"`
}

// RecordLimitEvents reads, from each Docker host the apptown runs standing
// deployments on, what its daemon reported since the last event recorded
// there (at most limitEventReach back), and records each out-of-memory
// kill and each exit of a standing deployment's part or need. A container
// AppTown does not run as a standing deployment's (a preview, a step,
// other software) is not recorded. A host that cannot be read is logged
// and read again next sweep. It prunes events older than limitEventKeep
// at most hourly.
func (s *Service) RecordLimitEvents(ctx context.Context) {
	// Every standing deployment, a release in progress among them: its
	// boot is when a newly limited container is likeliest to be killed.
	// Reading is all this does; the health controller's actions keep to
	// the settled ones.
	records, err := s.Deployments.All(ctx)
	if err != nil {
		log.Printf("limit events: reading the standing deployments: %v", err)
		return
	}
	byTarget := map[ids.ID][]ProjectDeployment{}
	for _, r := range records {
		if r.Kind == Standing && !r.TargetID.IsZero() {
			byTarget[r.TargetID] = append(byTarget[r.TargetID], r)
		}
	}
	for targetID, deps := range byTarget {
		if err := s.recordTargetLimitEvents(ctx, targetID, deps); err != nil {
			log.Printf("limit events: target %s: %v", targetID, err)
		}
	}
	s.pruneLimitEvents(ctx)
}

type partOf struct {
	deployment ids.ID
	part       string
}

// settledAs is what a sweep read of a deployment, to say which generation
// an event of its happened under: its identity label, and the generation it
// had settled on, from when until it was checked still settled. A
// deployment with a deploy under way, or one that failed part way, has no
// settled interval (generation 0): a rollout replaces its other parts before
// it settles, so an unlabelled event then is not known to be either
// generation's. Nor is one after the check: a rollout may have begun since.
type settledAs struct {
	label        string
	generation   int64
	since, until time.Time
}

// generationOf is the generation an event happened under: the one its
// container's labels name when they name this deployment (the public part's
// container is one generation's), else the settled one when the event came
// after it settled and no later than the check that found it still settled,
// else not known.
func (d settledAs) generationOf(e docker.ContainerEvent) *int64 {
	if e.Deployment != "" && e.Deployment == d.label {
		if g, err := strconv.ParseInt(e.Generation, 10, 64); err == nil && g > 0 {
			return &g
		}
	}
	if d.generation > 0 && !d.since.IsZero() && !e.At.Before(d.since) && !e.At.After(d.until) {
		g := d.generation
		return &g
	}
	return nil
}

// containerOwners answers which standing deployment's part or need a
// container on one target is, by the names AppTown gives them: the
// public part's container (whichever generation runs it), and each
// other part's and need's service on the deployment's stack.
type containerOwners struct {
	exact    map[string]partOf
	services map[string]ids.ID // a stack's service-name prefix → its deployment
	settled  map[ids.ID]settledAs
}

func (o containerOwners) of(container string) (partOf, bool) {
	if p, ok := o.exact[container]; ok {
		return p, true
	}
	// The longest stack prefix wins: main's services must not take
	// main-eu's (apptown-svc-env-shop-main- is a prefix of
	// apptown-svc-env-shop-main-eu-postgres).
	best := ""
	for prefix := range o.services {
		if len(prefix) > len(best) && strings.HasPrefix(container, prefix) && len(container) > len(prefix) {
			best = prefix
		}
	}
	if best == "" {
		return partOf{}, false
	}
	return partOf{deployment: o.services[best], part: strings.TrimPrefix(container, best)}, true
}

func (s *Service) ownersOn(ctx context.Context, deps []ProjectDeployment) (containerOwners, error) {
	o := containerOwners{exact: map[string]partOf{}, services: map[string]ids.ID{}, settled: map[ids.ID]settledAs{}}
	for _, d := range deps {
		applied, since, err := s.settled(ctx, &d.Deployment)
		if err != nil {
			return o, err
		}
		as := settledAs{label: string(d.Label())}
		checked := s.now()
		if why, err := s.unsettledReason(ctx, &d.Deployment); err != nil {
			return o, err
		} else if why == "" {
			as.generation, as.since, as.until = d.ObservedGeneration, since, checked
		}
		o.settled[d.ID] = as
		public := ""
		for name, p := range applied.Parts {
			if p.Public {
				public = name
			}
		}
		names, err := d.HostNames()
		if err != nil {
			return o, err
		}
		for _, c := range []string{d.Container, names.App()} {
			if c != "" {
				o.exact[c] = partOf{deployment: d.ID, part: public}
			}
		}
		o.services[names.Service("")] = d.ID
	}
	return o, nil
}

func (s *Service) recordTargetLimitEvents(ctx context.Context, targetID ids.ID, deps []ProjectDeployment) error {
	t, err := s.Targets.ByID(ctx, targetID)
	if err != nil {
		return err
	}
	// Only a Docker host's daemon is read, whether or not anything has
	// settled there yet (a first release's boot is when a kill matters):
	// App Platform reports none to AppTown, and Portainer's are not read
	// in 1.0.
	dk, err := s.hosts.DockerScope(ctx, t)
	if errors.Is(err, ErrNotDockerHost) {
		return nil
	}
	if err != nil {
		return err
	}
	// The read's cutoff is taken before each deployment's settled state is
	// checked, so the events read all come before the checks that
	// attribute them (settledAs.until).
	now := s.now()
	owners, err := s.ownersOn(ctx, deps)
	if err != nil {
		return err
	}
	frontier := now.Add(-limitEventReach)
	latest, err := deploymentsql.New(s.db.SQL()).LatestLimitEventOnTarget(ctx, targetID)
	if err != nil {
		return fmt.Errorf("reading the newest recorded event: %w", err)
	}
	if latest.After(frontier) {
		frontier = latest
	}
	// The read reaches back the time a stop and its exit may be apart, so
	// an exit at the frontier can still be told from one a stop caused.
	// What lies before the frontier is context only: it was judged by the
	// read that covered it, with its own context, and is never judged
	// again without it.
	events, err := docker.ContainerEvents(ctx, dk, frontier.Add(-stopPairing), now)
	if err != nil {
		return err
	}
	for _, e := range limitEventsOf(events, owners) {
		if e.At.Before(frontier) {
			continue
		}
		if err := s.recordLimitEvent(ctx, e.deployment, e.LimitEvent); err != nil {
			return err
		}
	}
	return nil
}

type ownedEvent struct {
	LimitEvent
	deployment ids.ID
}

// limitEventsOf is the events a sweep records: each out-of-memory kill of
// an owned container, and each exit of one that neither a kill (AppTown
// or Docker stopping it) nor an out-of-memory kill caused. Events of
// containers no standing deployment owns are not recorded.
func limitEventsOf(events []docker.ContainerEvent, owners containerOwners) []ownedEvent {
	stops := map[string][]docker.ContainerEvent{}
	for _, e := range events {
		if e.Action == "kill" || e.Action == "oom" {
			stops[e.Container] = append(stops[e.Container], e)
		}
	}
	causedBy := func(die docker.ContainerEvent) bool {
		for _, st := range stops[die.Container] {
			if d := die.At.Sub(st.At); d >= -stopPairing && d <= stopPairing {
				return true
			}
		}
		return false
	}
	var out []ownedEvent
	for _, e := range events {
		o, ok := owners.of(e.Container)
		if !ok {
			continue
		}
		ev := LimitEvent{Part: o.part, Container: e.Container, At: e.At, Generation: owners.settled[o.deployment].generationOf(e)}
		switch {
		case e.Action == "oom":
			ev.Kind = LimitOutOfMemory
		case e.Action == "die" && !causedBy(e):
			ev.Kind, ev.ExitCode = LimitExited, e.ExitCode
		default:
			continue
		}
		out = append(out, ownedEvent{LimitEvent: ev, deployment: o.deployment})
	}
	return out
}

func (s *Service) recordLimitEvent(ctx context.Context, deploymentID ids.ID, e LimitEvent) error {
	var code sql.NullInt32
	if e.ExitCode != nil {
		code = sql.NullInt32{Int32: int32(*e.ExitCode), Valid: true}
	}
	var generation sql.NullInt64
	if e.Generation != nil {
		generation = sql.NullInt64{Int64: *e.Generation, Valid: true}
	}
	_, err := deploymentsql.New(s.db.SQL()).RecordLimitEvent(ctx, deploymentsql.RecordLimitEventParams{
		ID: ids.New(s.now()), DeploymentID: deploymentID, Part: e.Part, Kind: e.Kind,
		Container: e.Container, At: e.At.UTC(), ExitCode: code, Generation: generation,
	})
	if err != nil {
		return fmt.Errorf("recording a limit event: %w", err)
	}
	return nil
}

func (s *Service) pruneLimitEvents(ctx context.Context) {
	now := s.now()
	s.limitsMu.Lock()
	due := now.Sub(s.limitsPruned) >= time.Hour
	if due {
		s.limitsPruned = now
	}
	s.limitsMu.Unlock()
	if !due {
		return
	}
	if _, err := deploymentsql.New(s.db.SQL()).PruneLimitEvents(ctx, now.Add(-limitEventKeep)); err != nil {
		log.Printf("limit events: pruning: %v", err)
	}
}

// LimitEvents are the limit events recorded for a deployment since a
// time, oldest first.
func (s *Service) LimitEvents(ctx context.Context, d *Deployment, since time.Time) ([]LimitEvent, error) {
	rows, err := deploymentsql.New(s.db.SQL()).LimitEventsSince(ctx, deploymentsql.LimitEventsSinceParams{DeploymentID: d.ID, At: since.UTC()})
	if err != nil {
		return nil, fmt.Errorf("reading the deployment's limit events: %w", err)
	}
	out := make([]LimitEvent, 0, len(rows))
	for _, r := range rows {
		e := LimitEvent{Part: r.Part, Kind: r.Kind, Container: r.Container, At: r.At.UTC()}
		if r.ExitCode.Valid {
			c := int(r.ExitCode.Int32)
			e.ExitCode = &c
		}
		if r.Generation.Valid {
			g := r.Generation.Int64
			e.Generation = &g
		}
		out = append(out, e)
	}
	return out, nil
}

// LimitCount is how often one kind of event happened to one part in a
// window, and when last.
type LimitCount struct {
	Part  string    `json:"part"`
	Kind  string    `json:"kind"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// LimitSummary counts a deployment's limit events per part and kind over
// the window before now. Where the events are read from is said by
// LimitEventsSource: an empty summary from a target that reports none is
// not "none happened".
func (s *Service) LimitSummary(ctx context.Context, d *Deployment, window time.Duration) ([]LimitCount, error) {
	events, err := s.LimitEvents(ctx, d, s.now().Add(-window))
	if err != nil {
		return nil, err
	}
	counts := map[[2]string]*LimitCount{}
	for _, e := range events {
		k := [2]string{e.Part, e.Kind}
		c, ok := counts[k]
		if !ok {
			c = &LimitCount{Part: e.Part, Kind: e.Kind}
			counts[k] = c
		}
		c.Count++
		if e.At.After(c.Last) {
			c.Last = e.At
		}
	}
	out := make([]LimitCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Part != out[j].Part {
			return out[i].Part < out[j].Part
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// LimitEventsSource says where a deployment's limit events come from, or
// why there are none to read: the words a view shows beside an empty list.
func (s *Service) LimitEventsSource(ctx context.Context, d *Deployment) (known bool, words string) {
	if d.TargetID.IsZero() {
		return false, "not deployed to a target"
	}
	t, err := s.Targets.ByID(ctx, d.TargetID)
	if err != nil {
		return false, "its target cannot be read"
	}
	switch t.Kind {
	case KindDockerHost:
		return true, "recorded from the Docker host's events; Docker replays only its recent events, so a gap is possible while AppTown or the daemon was down"
	case KindDOApp:
		return false, "App Platform does not report them to AppTown"
	case KindPortainer:
		return false, "not read through Portainer in 1.0"
	}
	return false, "this target runs no containers"
}
