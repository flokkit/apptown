package dataprotection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/s3bucket"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/stamp"
)

// Restore rehearsals (design/subsystems/deployment.md, "Restore
// rehearsals"). A drill
// proves a copy comes back into a database; a rehearsal proves the app works
// on it. It is an operation on the deployment it rehearses, which boots that
// deployment's current release from a chosen copy on a Docker host kept for
// rehearsals, asks the app its readiness, its front page and its declared
// data checks, records the answer on the deployment, and takes everything it
// made down again: the place is the operation's own, never a deployment, and
// takes no final copy (its data is a copy of a copy).
//
// The boundary is the host, not the container: the rehearsal publishes no
// port and joins no ingress, and only the forge's probe, inside its own
// network, asks it anything (docker.Sealed). Root, the Docker socket, any
// process on the host and any container on the host's network can still
// reach it, so a platform administrator keeps a dedicated host for
// rehearsals, told exactly that when marking it.
//
// What the forge records of a rehearsal is safe to show anyone who may read
// the project: step names, the commands that ran and how they exited, status
// codes, and the names of what was withheld. Never a command's output, a
// container's log or a response body: those can hold rows of production
// data.

var (
	// ErrNoRehearsalHost: no Docker host is kept for rehearsals.
	ErrNoRehearsalHost = fault.New(fault.Conflict, "no Docker host is kept for restore rehearsals: a platform administrator marks a dedicated attached Docker host for them")
	// ErrRehearsalNotStopped: a rehearsal runs to its end and takes itself
	// down; it is not stopped part way.
	ErrRehearsalNotStopped = fault.New(fault.Conflict, "a restore rehearsal is not stopped part way: it takes everything it made down when it ends")
	// ErrRehearsalSource: what the rehearsal would start from is missing.
	ErrRehearsalSource = fault.New(fault.Conflict, "this deployment cannot be rehearsed")
)

// Rehearsal outcomes, as recorded.
const (
	RehearsalPassed       = "passed"
	RehearsalPassedNoData = "passed: booted and ready; no data check declared"
	RehearsalFailed       = "failed"
)

// RehearsalCheck is one question the rehearsal asked the app and the status
// it answered (0: no answer).
type RehearsalCheck struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Passed bool   `json:"passed"`
}

// RehearsalFacts is what a rehearsal recorded, all of it safe for a project
// reader.
type RehearsalFacts struct {
	Checks     []RehearsalCheck  `json:"checks"`
	Withheld   []string          `json:"withheld"`
	NotStarted []string          `json:"notStarted"`
	Images     map[string]string `json:"images"`
	// Egress is said every time: the rehearsal blocks no outbound network.
	Egress string `json:"egress"`
	// Gate is a restored copy's check of its own gate (checkGate): what it
	// refused, and the forge's own ticket let in once. A rehearsal has no
	// gate and records none.
	Gate []RehearsalCheck `json:"gate,omitempty"`
}

// RehearsalRef is the last restore rehearsal of a deployment.
type RehearsalRef struct {
	At         stamp.Time `json:"at"`
	Outcome    string     `json:"outcome"`
	Passed     bool       `json:"passed"`
	Backup     ids.ID     `json:"backup"`
	Release    int64      `json:"release"`
	Host       string     `json:"host"`
	FailedStep string     `json:"failedStep,omitempty"`
	AskedBy    string     `json:"askedBy"`
	// AskedByPrincipal is the asker's principal; zero for the forge, or one
	// not resolved to a person.
	AskedByPrincipal ids.ID         `json:"askedByPrincipal,omitzero"`
	Operation        ids.ID         `json:"operation"`
	Facts            RehearsalFacts `json:"facts"`
	// Previous is the last recorded rehearsal before this one, when this one
	// is an interrupted or failed operation that recorded none: an older
	// pass is history, never the latest answer.
	Previous *RehearsalRef `json:"previous,omitempty"`
}

// RehearsalUnknown is the outcome of a rehearsal the forge stopped in the
// middle of: whether the app answered is not known.
const RehearsalUnknown = "unknown: AppTown stopped while it ran"

const egressNotBlocked = "outbound network not blocked"

// LastRehearsal is a deployment's last restore rehearsal, nil when it was
// never rehearsed. It is the latest rehearsal operation's answer: one that
// ended without recording a rehearsal (the forge stopped while it ran, or it
// failed before it could) reads unknown or failed, with the last recorded
// rehearsal kept beside it as history.
func (s *Service) LastRehearsal(ctx context.Context, projectID ids.ID, name string) (*RehearsalRef, error) {
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if found, lerr := database.Lookup(err); lerr != nil || !found {
		return nil, lerr
	}
	return s.lastRehearsalOf(ctx, dep)
}

// lastRehearsalOf reads a deployment's last rehearsal by the deployment
// the operations ran on, never by its name.
func (s *Service) lastRehearsalOf(ctx context.Context, dep *deployment.Deployment) (*RehearsalRef, error) {
	var recorded *RehearsalRef
	r, err := protectionsql.New(s.db.SQL()).LatestRestoreRehearsal(ctx, dep.ID)
	switch {
	case err == nil:
		recorded = &RehearsalRef{At: stamp.Of(r.RehearsedAt), Outcome: r.Outcome, Backup: r.BackupID, Release: r.ReleaseNumber, Host: r.Host,
			FailedStep: r.FailedStep, AskedBy: r.AskedBy, AskedByPrincipal: r.AskedByPrincipal, Operation: r.OperationID}
		recorded.Passed = r.Outcome == RehearsalPassed || r.Outcome == RehearsalPassedNoData
		_ = json.Unmarshal(r.FactsJson, &recorded.Facts)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	if s.ops == nil {
		return recorded, nil
	}
	o, err := s.ops.LatestEnded(ctx, deployment.SubjectOf(dep), OperationRehearsal)
	if err != nil {
		return nil, err
	}
	// One cancelled (only ever before it started) rehearsed nothing.
	if o == nil || o.State == operations.Cancelled || (recorded != nil && o.ID.Compare(recorded.Operation) <= 0) {
		return recorded, nil
	}
	at := o.StartedAt
	if !at.Valid {
		at = o.CreatedAt
	}
	ref := &RehearsalRef{At: at, Outcome: RehearsalFailed, AskedBy: o.AskedBy, Operation: o.ID, Previous: recorded,
		Facts: RehearsalFacts{Egress: egressNotBlocked, Withheld: []string{}, NotStarted: []string{}, Checks: []RehearsalCheck{}}}
	if o.State == operations.Unknown {
		ref.Outcome = RehearsalUnknown
	}
	ref.Backup = inputID(o.Input, "backup")
	if place, err := s.recordedPlace(ctx, o.ID); err != nil {
		return nil, err
	} else {
		ref.Host = place.Host
	}
	for _, st := range o.Steps {
		if st.Failed {
			ref.FailedStep = st.Name
			break
		}
	}
	return ref, nil
}

// rehearsalHost is the one Docker host kept for rehearsals.
func (s *Service) rehearsalHost(ctx context.Context) (*deployment.Target, error) {
	targets, err := s.targets.All(ctx)
	if err != nil {
		return nil, err
	}
	var kept []*deployment.Target
	for _, t := range targets {
		if t.ForRehearsals() && t.Status.Offerable() {
			kept = append(kept, t)
		}
	}
	switch len(kept) {
	case 0:
		return nil, ErrNoRehearsalHost
	case 1:
		return kept[0], nil
	}
	names := make([]string, len(kept))
	for i, t := range kept {
		names[i] = t.Name
	}
	return nil, fault.New(fault.Conflict, fmt.Sprintf("several Docker hosts are kept for restore rehearsals (%s); keep one", strings.Join(names, ", ")))
}

// rehearsalScope is how the forge reaches a rehearsal host's daemon.
func (s *Service) rehearsalScope(ctx context.Context, t *deployment.Target) (docker.Scope, error) {
	if s.hostScope != nil {
		return s.hostScope(ctx, t)
	}
	return s.hosts.DockerScope(ctx, t)
}

// rehearsalPlan is what a rehearsal of a deployment would start from: the
// deployment, the copy, the release it serves now and its images, and the
// host.
type rehearsalPlan struct {
	dep     *deployment.Deployment
	set     *Backup
	release *deployment.Release
	bundle  *deployment.ReleaseBundle
	host    *deployment.Target
}

// planRehearsal resolves what a rehearsal starts from, refusing with why.
// set 0 is the deployment's newest good copy in the store.
func (s *Service) planRehearsal(ctx context.Context, projectID ids.ID, name string, set ids.ID) (*rehearsalPlan, error) {
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if err != nil || !dep.Standing() {
		return nil, fault.New(fault.NotFound, fmt.Sprintf("no standing deployment named %q", name))
	}
	p := &rehearsalPlan{dep: dep}
	// The backups the forge takes itself (a Docker host's) are on its disk
	// until a store is attached; a rehearsal reads only the store.
	if t, err := s.targets.ByID(ctx, dep.TargetID); err == nil && t.HoldsBackups() == nil {
		store, err := s.Store(ctx)
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, ErrOnForgeDisk
		}
	}
	if set.IsZero() {
		if p.set, err = s.newestStoreSet(ctx, projectID, name); err != nil {
			return nil, err
		}
	} else {
		// Which of the set's items the release can be filled from is
		// decided when the release's contract is read (rehearse).
		if p.set, err = s.storeSet(ctx, set, ErrRehearsalSource); err != nil {
			return nil, err
		}
		if p.set.ProjectID != projectID || p.set.Deployment != name {
			return nil, fmt.Errorf("%w: set %d is a copy of another deployment", ErrRehearsalSource, set)
		}
	}
	if p.set.DataClass == "production" && dep.DataClass != "production" {
		return nil, deployment.ErrProductionCopy
	}
	number, err := s.releases.LatestReleasedNumber(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if number == 0 {
		return nil, fmt.Errorf("%w: %s has served no release", ErrRehearsalSource, name)
	}
	if p.release, err = s.releases.ByNumber(ctx, projectID, number); err != nil {
		return nil, err
	}
	if !p.release.BundleID.IsZero() {
		if p.bundle, err = s.bundles.Find(ctx, p.release.BundleID); err != nil {
			return nil, err
		}
	}
	if p.bundle == nil || !p.bundle.ByDigest() || p.bundle.ProjectID != projectID {
		return nil, fmt.Errorf("%w: release %d was not published to a registry, so another host cannot run its images", ErrRehearsalSource, number)
	}
	if p.host, err = s.rehearsalHost(ctx); err != nil {
		return nil, err
	}
	if _, err := rehearsalOrigin(dep); err != nil {
		return nil, err
	}
	return p, nil
}

// rehearsalOrigin is the address the deployment's visitors reach it at:
// the rehearsal's only APPTOWN_ORIGINS, and the Host its probe sends.
func rehearsalOrigin(dep *deployment.Deployment) (string, error) {
	u, err := url.Parse(dep.URL)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("%w: %s has no recorded address to rehearse it at", ErrRehearsalSource, dep.Name)
	}
	return u.Hostname(), nil
}

// RequestRehearsal starts a restore rehearsal of a standing deployment from
// set (0: its newest good copy in the store). Production data moves to
// another host, so it needs deployment:restore in the deployment's project;
// no agent carries it. The scheduled rehearsals start as the forge.
func (s *Service) RequestRehearsal(ctx context.Context, projectID ids.ID, name string, set ids.ID) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentRestore, projectID, "deployment.rehearse")
	if err != nil {
		return nil, err
	}
	if err := project.RefuseUnlessActive(ctx, s.db, projectID); err != nil {
		return nil, err
	}
	p, err := s.planRehearsal(ctx, projectID, name, set)
	if err != nil {
		return nil, err
	}
	return s.admit(ctx, p.dep, OperationRehearsal, actor, []string{deployment.DataHold(p.dep)}, map[string]any{"backup": p.set.ID}, nil)
}

// rehearsalPlace names what a rehearsal makes, all of it on the rehearsal
// host, from its operation: the volume its copy is staged in, its stack
// (needs, init steps, network) and its app container with its data volume.
// Nothing of the copy is ever written to the forge's own disk, where a
// project's script steps run: it streams from the store through the
// forge's memory into the host's volume.
type rehearsalPlace struct {
	Copy  string `json:"copy"`
	Host  string `json:"host"`
	Stack string `json:"stack"`
	App   string `json:"app"`
}

func placeOf(op ids.ID, host string) rehearsalPlace {
	n := op.String()
	return rehearsalPlace{Copy: "forge-rehearsal-" + n + "-copy", Host: host, Stack: "rehearsal-" + n, App: "forge-rehearsal-" + n}
}

// copyMount is where a rehearsal's staged copy is mounted in the jobs that
// write and read it.
const copyMount = "/copy"

// clear removes everything the place holds; what could not be removed is
// named, never reported gone.
func (s *Service) clearPlace(ctx context.Context, place rehearsalPlace) []string {
	var left []string
	if place.Host == "" {
		return left
	}
	t, err := s.targets.ByName(ctx, place.Host)
	if err != nil {
		return append(left, fmt.Sprintf("the rehearsal on %s: the host could not be read: %v", place.Host, err))
	}
	dk, err := s.rehearsalScope(ctx, t)
	if err != nil {
		return append(left, fmt.Sprintf("the rehearsal on %s: the host could not be reached: %v", place.Host, err))
	}
	if err := dk.RemoveDisposable(ctx, place.App); err != nil {
		left = append(left, fmt.Sprintf("the app container %s on %s: %v", place.App, place.Host, err))
	}
	if err := dk.RemoveDisposableStack(ctx, place.Stack); err != nil {
		left = append(left, fmt.Sprintf("the stack %s on %s: %v", place.Stack, place.Host, err))
	}
	if place.Copy != "" {
		if err := dk.RemoveVolume(ctx, place.Copy); err != nil {
			left = append(left, fmt.Sprintf("the staged copy %s on %s: %v", place.Copy, place.Host, err))
		}
	}
	return left
}

// SweepRehearsals removes what every ended rehearsal still owns: one the
// forge stopped in the middle of (settled unknown at start), or whose
// take-down left something. Each place is marked cleared only when nothing
// is left; one whose operation is still live is left to it.
func (s *Service) SweepRehearsals(ctx context.Context) (cleared int, left []string, err error) {
	q := protectionsql.New(s.db.SQL())
	rows, err := q.UnclearedRehearsalPlaces(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range rows {
		if s.ops != nil {
			op, err := s.ops.ByID(ctx, r.OperationID)
			if err != nil && !errors.Is(err, operations.ErrMissing) {
				return cleared, left, err
			}
			if op != nil && op.State.Live() {
				continue
			}
		}
		var place rehearsalPlace
		if err := json.Unmarshal(r.PlaceJson, &place); err != nil {
			return cleared, left, fmt.Errorf("rehearsal %d's place: %w", r.OperationID, err)
		}
		if rest := s.clearPlace(ctx, place); len(rest) > 0 {
			left = append(left, fmt.Sprintf("operation %d: %s", r.OperationID, strings.Join(rest, "; ")))
			continue
		}
		if _, err := q.MarkRehearsalCleared(ctx, protectionsql.MarkRehearsalClearedParams{OperationID: r.OperationID, At: s.now()}); err != nil {
			return cleared, left, err
		}
		cleared++
	}
	return cleared, left, nil
}

// recordedPlace is the place a rehearsal recorded making; zero when it
// made nothing.
func (s *Service) recordedPlace(ctx context.Context, operation ids.ID) (rehearsalPlace, error) {
	var p rehearsalPlace
	r, err := protectionsql.New(s.db.SQL()).RehearsalPlaceOf(ctx, operation)
	if found, err := database.Lookup(err); err != nil || !found {
		return p, err
	}
	return p, json.Unmarshal(r.PlaceJson, &p)
}

// safeFailure is a rehearsal failure in words safe for a project reader: a
// step and what ran, never what it printed.
type safeFailure struct{ text string }

func (e *safeFailure) Error() string { return e.text }

// exitOf names how a command ended, from its error, without its output:
// the process's own exit, wherever it sits in the error's chain (a copy
// job wraps it with what the command printed, and fill wraps that again).
func exitOf(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if m := exitStatus.FindString(e.Error()); m != "" {
			return m
		}
	}
	return "it did not finish"
}

var (
	exitStatus = regexp.MustCompile(`^exit status \d+`)
	initName   = regexp.MustCompile(`^init "([^"]+)" `)
)

// bootFailure words a boot's failure by its phase, never by its text.
func bootFailure(err error, readiness string) string {
	be := docker.AsBootError(err)
	var safe *safeFailure
	if errors.As(err, &safe) {
		return safe.text
	}
	if errors.Is(err, secret.ErrRehearsalInherits) {
		return secret.ErrRehearsalInherits.Error()
	}
	// The catalogue's refusal of the release's needs is the forge's own
	// sentence, naming the declaration, never a value.
	var refused *dependency.Refused
	var unknown *dependency.UnknownNeeds
	if errors.As(err, &refused) {
		return "the release's needs are refused as declared: " + refused.Why
	}
	if errors.As(err, &unknown) {
		return "the release's needs are refused as declared: " + unknown.Error()
	}
	switch be.Phase {
	case docker.PhaseResolve, docker.PhaseConfig:
		return "the release's images or needs could not be prepared on the host"
	case docker.PhaseSecrets:
		return "the app's secrets could not be read for the rehearsal"
	case docker.PhaseNeeds:
		return "the app's needs did not start"
	case docker.PhaseFill:
		return "the copy could not be filled into the new needs"
	case docker.PhaseParts:
		if m := initName.FindStringSubmatch(be.Err.Error()); m != nil {
			return fmt.Sprintf("the init step %q did not finish", m[1])
		}
		return "an init step did not finish"
	case docker.PhaseApp:
		return "the app container did not start"
	case docker.PhaseProbe:
		return fmt.Sprintf("the app did not answer its readiness path %s on its own network", readiness)
	}
	return "the boot did not finish"
}

// rehearsalSecrets resolves a rehearsal's secrets against the bindings of
// the deployment it rehearses, and keeps the names it withheld.
type rehearsalSecrets struct {
	secrets  *secret.Service
	project  ids.ID
	source   string
	withheld *[]string
}

func (r rehearsalSecrets) Resolve(ctx context.Context, _, _, ws string, c *appcontract.Contract) (docker.Delivery, error) {
	got, err := r.secrets.ResolveRehearsal(ctx, r.project, r.source, ws, c)
	if err != nil {
		return docker.Delivery{}, err
	}
	*r.withheld = got.Withheld
	return got.Delivery.Docker(), nil
}

// sealedRoutes: a rehearsal's app trusts only the address its visitors
// reach the deployment it stands in for, and its probe arrives as through a
// front door.
type sealedRoutes struct{ origin string }

func (r sealedRoutes) FrontDoorHeaders() map[string]string {
	return map[string]string{"X-Forwarded-Proto": "https"}
}
func (r sealedRoutes) PublicOrigins(ctx context.Context) string { return "https://" + r.origin }

func (s *Service) runRehearsal(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	set := inputID(t.op.Input, "backup")
	result := map[string]any{"backup": set}
	facts := RehearsalFacts{Egress: egressNotBlocked, Withheld: []string{}, NotStarted: []string{}, Checks: []RehearsalCheck{}}
	t.begin(ctx, "choosing the copy, the release and the host")
	p, err := s.planRehearsal(ctx, t.op.ProjectID, t.op.Deployment, set)
	if err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}
	place := placeOf(t.op.ID, p.host.Name)
	facts.Images = p.bundle.Parts
	t.end(ctx, fmt.Sprintf("set %d, release %d, on %s", p.set.ID, p.release.Number, p.host.Name), 0)

	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}
	// What it makes is recorded before it is made, so a restart's sweep
	// finds it.
	raw, err := json.Marshal(place)
	if err == nil {
		err = protectionsql.New(s.db.SQL()).RecordRehearsalPlace(ctx, protectionsql.RecordRehearsalPlaceParams{OperationID: t.op.ID, ProjectID: t.op.ProjectID, PlaceJson: raw, At: s.now()})
	}
	if err != nil {
		return OperationFailed, "nothing was made: recording the rehearsal's place: " + err.Error(), result
	}
	result["place"] = place
	state, failedStep, detail := s.rehearse(ctx, t, p, place, &facts)

	t.begin(ctx, "taking the rehearsal down")
	// The rehearsal's place comes down though the operation was stopped.
	left := s.clearPlace(context.WithoutCancel(ctx), place)
	if len(left) > 0 {
		t.fail(ctx, "left: "+strings.Join(left, "; "))
		result["left"] = left
	} else {
		t.end(ctx, "", 0)
		cleared := s.now().UTC()
		if _, err := protectionsql.New(s.db.SQL()).MarkRehearsalCleared(context.WithoutCancel(ctx), protectionsql.MarkRehearsalClearedParams{OperationID: t.op.ID, At: cleared}); err != nil {
			// The sweep finds it again and finds nothing left.
			result["clearedUnrecorded"] = err.Error()
		}
		result["cleared"] = cleared.Format(time.RFC3339)
	}

	outcome := RehearsalFailed
	if state == OperationVerified {
		outcome = RehearsalPassed
		if !slices.ContainsFunc(facts.Checks, func(c RehearsalCheck) bool { return strings.HasPrefix(c.Name, "data ") }) {
			outcome = RehearsalPassedNoData
		}
	}
	if err := s.recordRehearsal(ctx, t, p, outcome, failedStep, facts); err != nil {
		return OperationFailed, "the rehearsal ran but was not recorded: " + err.Error(), result
	}
	result["outcome"] = outcome
	if len(left) > 0 {
		detail += " Not everything it made was removed; the daily sweep tries again: " + strings.Join(left, "; ")
		if state == OperationVerified {
			state = OperationFailed
		}
	}
	return state, detail, result
}

// rehearse fills, boots and checks; it says how it went, and the step a
// failure was in, in safe words.
func (s *Service) rehearse(ctx context.Context, t *tracker, p *rehearsalPlan, place rehearsalPlace, facts *RehearsalFacts) (runEnd, string, string) {
	fail := func(step, why string) (runEnd, string, string) {
		t.fail(ctx, why)
		return OperationFailed, step, fmt.Sprintf("the rehearsal of %s from set %d failed while %s: %s", p.dep.Name, p.set.ID, step, why)
	}
	step := "reading the release's source"
	t.begin(ctx, step)
	proj, err := s.projects.ByID(ctx, p.dep.ProjectID)
	if err != nil {
		return fail(step, "the project could not be read")
	}
	tree, cleanup, err := (deploy.SelectedSource{Repository: s.hosts.ProjectRepository(proj.Slug), Revision: p.bundle.ContractRevision}).Stage(ctx, "")
	if err != nil {
		return fail(step, "the release's source could not be staged")
	}
	defer cleanup()
	c, err := docker.ReadSelectedContract(tree.Directory)
	if err != nil {
		return fail(step, "the release's .apptown/app.yaml could not be read")
	}
	for _, part := range c.Parts {
		if !part.Public {
			facts.NotStarted = append(facts.NotStarted, part.Name)
		}
	}

	// Every good item of the set is accounted for: each need's portable
	// copy fills the new need, the app's files fill its new data volume,
	// and a need the release declares disposable starts empty. An item
	// that can be none of these fails the rehearsal by name; nothing is
	// left behind silently.
	type move struct {
		from *BackupItem
		kind *dependency.Kind // nil: the app's files
	}
	var moves []move
	var cannot []string
	used := map[int]bool{}
	for _, need := range c.Needs {
		var from *BackupItem
		for i := range p.set.Items {
			if it := &p.set.Items[i]; it.Kind == "need" && it.Name == need && it.Error == "" {
				from, used[i] = it, true
			}
		}
		kind, kerr := s.catalog.ByCapability(ctx, need)
		switch {
		case c.Disposable(need):
			continue
		case from == nil:
			cannot = append(cannot, fmt.Sprintf("the app needs %s, which set %d holds no copy of and the app does not declare disposable", need, p.set.ID))
		case from.Format == "":
			cannot = append(cannot, fmt.Sprintf("set %d's copy of %s has no portable format, so a new %s cannot be filled from it (declare it disposable to rehearse without it)", p.set.ID, need, need))
		case kerr != nil || kind.Copy == nil || kind.Copy.Format != from.Format:
			cannot = append(cannot, fmt.Sprintf("the %s kind does not read the copy's format %s", need, from.Format))
		default:
			moves = append(moves, move{from, kind})
		}
	}
	for i := range p.set.Items {
		it := &p.set.Items[i]
		switch {
		case it.Error != "" || used[i]:
		case it.Kind == "volume" && c.PublicPart().DataDir != "":
			moves = append(moves, move{from: it})
		case it.Kind == "volume":
			cannot = append(cannot, fmt.Sprintf("set %d holds the app's files, and release %d declares no dataDir to put them in", p.set.ID, p.release.Number))
		default:
			cannot = append(cannot, fmt.Sprintf("set %d holds a copy of %s, which release %d does not need", p.set.ID, it.Name, p.release.Number))
		}
	}
	if len(cannot) > 0 {
		return fail(step, strings.Join(cannot, "; "))
	}
	t.end(ctx, "", 0)

	host := p.host
	dk, err := s.rehearsalScope(ctx, host)
	if err != nil {
		return fail("reaching the host", fmt.Sprintf("%s could not be reached", host.Name))
	}
	c2, store, err := s.storeOf(ctx, p.set)
	if err != nil {
		return fail("reading the copy back", "the backup store could not be reached: "+err.Error())
	}
	for _, m := range moves {
		step = fmt.Sprintf("copying set %d's copy of %s from the store to %s", p.set.ID, m.from.Name, host.Name)
		if err := t.allowed(ctx); err != nil {
			return OperationFailed, step, fmt.Sprintf("the rehearsal of %s stopped before %s: %v", p.dep.Name, step, err)
		}
		t.begin(ctx, step)
		if err := s.stageOnHost(ctx, dk, c2, store.Bucket, p.set, m.from, place.Copy); err != nil {
			return fail(step, err.Error())
		}
		t.end(ctx, fmt.Sprintf("%d bytes, sha256 %s, as recorded", m.from.Bytes, shortDigest(m.from.SHA256)), m.from.Bytes)
	}

	origin, _ := rehearsalOrigin(p.dep)
	// The rehearsal's needs hold a copy of the deployment's data: they get
	// the operation's own credentials, and its fill connects with them.
	creds := s.env.NeedCredentials(operationNeedScope(t.op.ID))
	sealed := &docker.Sealed{ProbeImage: s.env.ChownImage(), Host: origin}
	sealed.Fill = func(network string) error {
		for _, m := range moves {
			if m.kind == nil {
				// The app's files go into its new data volume before it
				// starts, as an in-place restore unpacks them.
				step = "filling the app's files from the copy"
				if err := t.allowed(ctx); err != nil {
					return &safeFailure{"authority withdrawn before " + step}
				}
				t.begin(ctx, step)
				args := []string{"run", "--rm", "--network", "none", "--entrypoint", "", "-v", place.Copy + ":" + copyMount + ":ro", "-v", docker.DataVolumeName(place.App) + ":/dst"}
				args = append(args, dk.Labels("step", "rehearsal-files")...)
				args = append(args, s.env.ChownImage(), "sh", "-c", "find /dst -mindepth 1 -delete && tar xz -C /dst < "+copyMount+"/"+m.from.File)
				if _, err := dk.Command(ctx, docker.Command{Args: args}); err != nil {
					return &safeFailure{fmt.Sprintf("unpacking the app's files ended with %s", exitOf(err))}
				}
				t.end(ctx, "", 0)
				continue
			}
			generated, err := m.kind.Generated(ctx, creds)
			if err != nil {
				return &safeFailure{fmt.Sprintf("the new %s's credentials: %v", m.from.Name, err)}
			}
			conn, version := m.kind.LocalConnection(c.NeedParams[m.from.Name], generated)
			image := clientImage(m.kind.Copy, version)
			step = fmt.Sprintf("filling the new %s from the copy, and checking it", m.from.Name)
			if err := t.allowed(ctx); err != nil {
				return &safeFailure{"authority withdrawn before " + step}
			}
			t.begin(ctx, step)
			for _, phase := range []struct{ name, command string }{{"import", m.kind.Copy.Import}, {"verify", m.kind.Copy.Verify}} {
				// The copy is read from the host's volume on the job's stdin,
				// as the kind's commands expect it.
				command := "exec < " + copyMount + "/" + m.from.File + "\n" + phase.command
				_, err := copyJobOn(ctx, dk, []string{"--network", network, "-v", place.Copy + ":" + copyMount + ":ro"}, m.kind.Copy, image, command, conn, nil, nil)
				if err != nil {
					return &safeFailure{fmt.Sprintf("the %s kind's %s ended with %s", m.from.Name, phase.name, exitOf(err))}
				}
			}
			t.end(ctx, fmt.Sprintf("the %s check passed", m.from.Format), 0)
		}
		step = "starting the app"
		if err := t.allowed(ctx); err != nil {
			return &safeFailure{"authority withdrawn before " + step}
		}
		t.begin(ctx, step)
		return nil
	}
	step = fmt.Sprintf("starting the needs of release %d on %s", p.release.Number, host.Name)
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, step, fmt.Sprintf("the rehearsal of %s stopped before %s: %v", p.dep.Name, step, err)
	}
	t.begin(ctx, step)
	var withheld []string
	deps := s.env.SealedHost(p.dep.ProjectID, rehearsalSecrets{secrets: s.secrets, project: p.dep.ProjectID, source: p.dep.Name, withheld: &withheld},
		sealedRoutes{origin: origin}, creds)
	readiness := docker.RenderColdBootProbe(c.PublicPart(), nil).Path
	if readiness == "" {
		readiness = "/"
	}
	req := docker.BootRequest{Name: place.App, StackID: place.Stack, Workspace: tree.Directory, Slug: proj.Slug, Deployment: p.dep.Name,
		DataClass: "production", Docker: dk, Probe: 90 * time.Second, Sealed: sealed,
		Recorded: &docker.RecordedImages{Parts: p.bundle.Parts, Init: p.bundle.Init, Login: s.env.BundleLogin(ctx, p.dep.ProjectID, p.bundle), Scratch: s.hosts.BootScratch(), SelfServing: p.bundle.BuiltFrom.SelfServing}}
	// The rehearsed release runs as its generation ran it: that
	// generation's profile and overrides (its secrets stay the rehearsal's
	// allow-list, rehearsalSecrets).
	applied, err := s.releases.AppliedOf(ctx, p.release.ID)
	if err != nil {
		return fail(step, err.Error())
	}
	res, err := docker.NewHost(deps).Boot(ctx, req, applied.On(c))
	facts.Withheld = append(facts.Withheld, withheld...)
	if err != nil {
		return fail(step, bootFailure(err, readiness))
	}
	t.end(ctx, "", 0)

	step = "asking the app"
	t.begin(ctx, step)
	port := res.Stack.Plan.PublicPart().Port
	ask := func(name, path string, judge func(int) bool) bool {
		var status int
		var err error
		if path == "" {
			status, err = sealed.Page(ctx, dk, res.Network, place.App, port)
			path = "/"
		} else {
			status, err = sealed.Get(ctx, dk, res.Network, place.App, port, path)
		}
		if err != nil {
			status = 0
		}
		ok := err == nil && judge(status)
		facts.Checks = append(facts.Checks, RehearsalCheck{Name: name, Path: path, Status: status, Passed: ok})
		return ok
	}
	rp := docker.RenderColdBootProbe(c.PublicPart(), nil)
	passed := ask("readiness", readiness, func(n int) bool { return rp.Expect == 0 || n == rp.Expect })
	passed = ask("page", "", func(n int) bool { return check.PageRefusal(n, "/") == "" }) && passed
	for _, line := range c.RehearsalChecks {
		path, expect, err := appcontract.ParseDataCheck(line)
		if err != nil {
			continue // the contract parser refused it already
		}
		passed = ask("data "+line, path, func(n int) bool { return n == expect }) && passed
	}
	var answers []string
	for _, ch := range facts.Checks {
		answers = append(answers, fmt.Sprintf("%s %d", ch.Path, ch.Status))
	}
	if !passed {
		return fail(step, "not every check passed: "+strings.Join(answers, ", "))
	}
	t.end(ctx, strings.Join(answers, ", "), 0)
	return OperationVerified, "", fmt.Sprintf("release %d ran on set %d's data on %s and answered %s", p.release.Number, p.set.ID, host.Name, strings.Join(answers, ", "))
}

// stageOnHost streams an item of a store set into a new volume on the
// rehearsal host, through the forge's memory, never its disk, and holds it
// to the set's record: its size and digest. A copy that does not match is
// not used.
func (s *Service) stageOnHost(ctx context.Context, dk docker.Scope, c *s3bucket.Client, bucket string, b *Backup, it *BackupItem, volume string) error {
	pr, pw := io.Pipe()
	type got struct {
		n   int64
		sum string
		err error
	}
	read := make(chan got, 1)
	go func() {
		n, sum, err := c.GetTo(ctx, bucket, b.Path+"/"+it.File, pw)
		pw.CloseWithError(err)
		read <- got{n, sum, err}
	}()
	args := []string{"run", "--rm", "-i", "--network", "none", "--entrypoint", "", "-v", volume + ":" + copyMount}
	args = append(args, dk.Labels("step", "rehearsal-copy")...)
	args = append(args, s.env.ChownImage(), "sh", "-c", "umask 077 && cat > "+copyMount+"/"+it.File)
	_, werr := dk.Command(ctx, docker.Command{Args: args, Stdin: pr})
	_ = pr.CloseWithError(werr)
	r := <-read
	switch {
	case r.err != nil:
		return r.err // the store's words
	case werr != nil:
		return fmt.Errorf("the copy could not be written to the host (%s)", exitOf(werr))
	case r.n != it.Bytes || r.sum != it.SHA256:
		return fmt.Errorf("the store holds %d bytes with sha256 %s for %s; the set records %d bytes with sha256 %s", r.n, shortDigest(r.sum), it.File, it.Bytes, shortDigest(it.SHA256))
	}
	return nil
}

// recordRehearsal writes the rehearsal's record and its receipt together.
func (s *Service) recordRehearsal(ctx context.Context, t *tracker, p *rehearsalPlan, outcome, failedStep string, facts RehearsalFacts) error {
	raw, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		at := s.now()
		asker := t.op.Authority.Actor()
		if _, err := protectionsql.New(tx.SQL()).InsertRestoreRehearsal(ctx, protectionsql.InsertRestoreRehearsalParams{ID: ids.New(at), ProjectID: p.dep.ProjectID, Deployment: p.dep.Name, DeploymentID: p.dep.ID,
			OperationID: t.op.ID, BackupID: p.set.ID, ReleaseNumber: p.release.Number, Host: p.host.Name, Outcome: outcome, FailedStep: failedStep, AskedBy: t.op.AskedBy, AskedByPrincipal: asker.Principal, AskedByVia: asker.Via, FactsJson: raw, At: at}); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, t.op.AskedBy, p.dep.ProjectID, "deployment.rehearsed", p.dep.Name, map[string]any{
			"operation": t.op.ID, "outcome": outcome, "failedStep": failedStep, "backup": p.set.ID, "backupDigests": setDigests(p.set),
			"release": p.release.Number, "images": facts.Images, "host": p.host.Name, "checks": facts.Checks,
			"withheld": facts.Withheld, "notStarted": facts.NotStarted, "egress": facts.Egress})
	})
}

// setDigests are each copy's digest in a set, by need.
func setDigests(b *Backup) map[string]string {
	out := map[string]string{}
	for _, it := range b.Items {
		if it.SHA256 != "" {
			out[it.Name] = it.SHA256
		}
	}
	return out
}
