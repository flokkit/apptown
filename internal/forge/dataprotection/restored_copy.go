package dataprotection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/gate"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/stamp"
)

// Restored copies (design/subsystems/deployment.md, "Restored copies"). A rehearsal
// proves a copy comes back on a sealed Docker host; a restored copy proves
// production comes back on its real runtime, and lets the people who may
// see production data look at it as it was.
//
// A restored copy is a temporary deployment (kind restored-copy) of a
// production deployment's current release, by digest, on the source's own
// target, started from a chosen copy, behind the forge's access gate. The
// gate is its only public component (deploy.Gate): it admits the forge's
// own checks, which carry the copy's gate secret, and people the forge let
// in with a one-use ticket from Open. The app's own sign-in still applies
// behind it.
//
// A restore test is a restored copy with no lifetime: nobody is admitted,
// and it is taken down as soon as its checks are recorded. An opened copy
// has a lifetime and is taken down when it runs out (the sweep), or when a
// person takes it down.
//
// What the forge records of a restored copy is safe to show anyone who may
// read the project: step names, the platform's own sentences, status codes
// and the names of what was withheld. Never a command's output, a
// component's log or a response body: those can hold rows of production
// data. The gate secret is never recorded: the row keeps its SHA-256, so a
// gate's redemption can be told to be this copy's.

// The lifetimes a restored copy may be given, and how long one Open lets a
// person in before they open it again (and the forge checks again).
const (
	RestoredCopyDefault = 4 * time.Hour
	RestoredCopyMost    = 24 * time.Hour
	GateSessionMost     = time.Hour
)

// RestoredCopyWarning is what the card says of what a restored copy can
// still do outside: withholding secrets reduces outside effects, it does
// not prevent them.
const RestoredCopyWarning = "AppTown integration secrets are withheld and background workers are stopped. Outbound calls are not blocked: this copy can still send mail, payments or webhooks using configuration or credentials in its data."

// ProviderBoundary names who outside the forge can read a restored copy.
const ProviderBoundary = "DigitalOcean team Owners and Members with console access can read this copy, its gate secret and its logs, as they can production's."

var (
	// ErrRestoredCopy: a restored copy cannot be made, opened or changed.
	ErrRestoredCopy = fault.New(fault.Conflict, "this restored copy cannot do that")
	// ErrNoGate: this forge has no gate image, so it makes no restored copy.
	ErrNoGate = fault.New(fault.Conflict, "this AppTown has no access gate image: start it with -restore-gate-image <ref@sha256:...> and its public address, so restored copies have a door only AppTown opens")
	// ErrGateCredential refuses a redemption that does not come from a
	// standing copy's gate.
	ErrGateCredential = fault.New(fault.Forbidden, "not a restored copy's gate")
	// ErrTicket refuses a ticket that was used, has expired, or is not this
	// copy's.
	ErrTicket = fault.New(fault.Forbidden, "this link was already used or has expired; press Open again in AppTown")
	// ErrRestoreNotStopped: a restored copy's start runs to its end; what
	// it made is taken down from the card, or when its time runs out.
	ErrRestoreNotStopped = fault.New(fault.Conflict, "a restored copy's start is not stopped part way: take the copy down from the card once it has started")
	// ErrRestoredCopyEndNotStopped: a restored copy's take-down runs to its
	// end; one stopped half way would leave production data standing.
	ErrRestoredCopyEndNotStopped = fault.New(fault.Conflict, "a restored copy's take-down is not stopped part way: stopping it would leave production data standing")
	// ErrRestoredCopiesStand: a deployment with a restored copy standing is
	// not taken down.
	ErrRestoredCopiesStand = fault.New(fault.Conflict, "restored copies of this deployment still stand")
	// ErrBrowserOnly: a ticket is issued only to a person's browser
	// session, never to a token or an agent.
	ErrBrowserOnly = fault.New(fault.Forbidden, "a restored copy is opened from AppTown in a browser, as yourself")
)

// RestoredCopyDependencies is what restored copies ask of the world.
type RestoredCopyDependencies struct {
	// GateImage is the access gate's image, pinned by digest; "" refuses
	// restored copies.
	GateImage string
	// Forge is the forge's public origin, where gates redeem tickets.
	Forge string
	// Secrets gives a restored copy only what its rehearsal profile names.
	Secrets RestoredCopySecrets
	// HTTP asks the app through its gate; nil is a client of its own.
	HTTP *http.Client
	// Grace is how long the checks wait for the app to answer; 0 is 3 minutes.
	Grace time.Duration
	// TicketFor is how long a ticket from Open lasts; 0 is a minute.
	TicketFor time.Duration
}

// RestoredCopySecrets resolves a restored copy's secrets against the
// bindings of the deployment it copies, by the rehearsal profile rule: a
// bound secret only when the profile (which must not inherit) names it,
// generated ones made fresh, nothing required. Withheld names each secret
// it was not given.
type RestoredCopySecrets interface {
	ResolveRestoredCopy(ctx context.Context, project deployment.Application, source, staged string, c *appcontract.Contract) (secrets deploy.ConsumerSecrets, withheld []string, err error)
}

// RestoredCopy is a restored copy as its source's card shows it.
type RestoredCopy struct {
	ID ids.ID `json:"id"`
	// Number names it within its project, as people read it ("restored
	// copy 4"); its deployment is copy-<number>.
	Number     int64  `json:"number"`
	Source     string `json:"source"`
	Deployment string `json:"deployment,omitempty"`
	Backup     ids.ID `json:"backup"`
	Release    int64  `json:"release,omitempty"`
	Target     string `json:"target,omitempty"`
	Test       bool   `json:"test"`
	AskedBy    string `json:"askedBy"`
	// AskedByPrincipal is the asker's principal; zero for the forge, or one
	// not resolved to a person.
	AskedByPrincipal ids.ID     `json:"askedByPrincipal,omitzero"`
	StartedAt        stamp.Time `json:"startedAt"`
	ExpiresAt        stamp.Time `json:"expiresAt,omitzero"`
	// Open says people may enter it now: it stands, it is not a test, its
	// checks found it ready and its time has not run out.
	Open      bool       `json:"open"`
	Outcome   string     `json:"outcome,omitempty"`
	FailedAt  string     `json:"failedStep,omitempty"`
	CheckedAt stamp.Time `json:"checkedAt,omitzero"`
	// EndedAt is when its take-down was seen to remove it; EndDetail says
	// how it ended, or what a take-down left.
	EndedAt   stamp.Time     `json:"endedAt,omitzero"`
	EndDetail string         `json:"endDetail,omitempty"`
	Facts     RehearsalFacts `json:"facts"`
	Entries   []GateEntry    `json:"entries"`
	Operation ids.ID         `json:"operation"`
	// Warning and Boundary are said on every card of a copy.
	Warning  string `json:"warning"`
	Boundary string `json:"boundary"`
	// Protection names what stands between the internet and the app, once
	// the copy was checked through its gate.
	Protection string `json:"protection,omitempty"`
}

// GateEntry is one person let in through the gate.
type GateEntry struct {
	Person string `json:"person"`
	// PersonPrincipal is the person's principal; zero when not resolved to one.
	PersonPrincipal ids.ID     `json:"personPrincipal,omitzero"`
	At              stamp.Time `json:"at"`
}

func (s *Service) restoredCopyRecord(ctx context.Context, r protectionsql.RestoredCopy) (*RestoredCopy, error) {
	c := &RestoredCopy{ID: r.ID, Number: r.Number, Source: r.Source, Deployment: r.Deployment, Backup: r.BackupID, Release: r.ReleaseNumber, Target: r.Target,
		Test: r.IsTest, AskedBy: r.AskedBy, AskedByPrincipal: r.AskedByPrincipal, StartedAt: stamp.Of(r.StartedAt), ExpiresAt: stamp.Time{NullTime: r.ExpiresAt},
		Outcome: r.Outcome, FailedAt: r.FailedStep, CheckedAt: stamp.Time{NullTime: r.CheckedAt}, EndedAt: stamp.Time{NullTime: r.EndedAt},
		EndDetail: r.EndDetail, Operation: r.OperationID, Entries: []GateEntry{}, Warning: RestoredCopyWarning, Boundary: ProviderBoundary}
	if err := json.Unmarshal(r.FactsJson, &c.Facts); err != nil {
		return nil, fmt.Errorf("restored copy %d facts: %w", r.ID, err)
	}
	if c.Facts.Checks == nil {
		c.Facts.Checks = []RehearsalCheck{}
	}
	c.Open = !r.IsTest && !r.EndedAt.Valid && r.CheckedAt.Valid && readinessPassed(c.Facts) && gateHeld(c.Facts) && r.GateUrl != "" &&
		r.ExpiresAt.Valid && s.now().Before(r.ExpiresAt.Time)
	if r.CheckedAt.Valid && readinessPassed(c.Facts) && gateHeld(c.Facts) {
		c.Protection = "AppTown gate, then the app's own sign-in where it has one"
	}
	rows, err := protectionsql.New(s.db.SQL()).RestoredCopyEntries(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range rows {
		c.Entries = append(c.Entries, GateEntry{Person: e.Person, PersonPrincipal: e.PersonPrincipal, At: stamp.Time{NullTime: e.RedeemedAt}})
	}
	return c, nil
}

// gateHeld says the forge checked the copy's gate and every check passed.
func gateHeld(f RehearsalFacts) bool {
	return len(f.Gate) > 0 && !slices.ContainsFunc(f.Gate, func(c RehearsalCheck) bool { return !c.Passed })
}

func readinessPassed(f RehearsalFacts) bool {
	for _, ch := range f.Checks {
		if ch.Name == "readiness" {
			return ch.Passed
		}
	}
	return false
}

// RestoredCopiesOf are a deployment's restored copies, newest first: every
// one standing, and the last limit ended.
func (s *Service) RestoredCopiesOf(ctx context.Context, dep *deployment.Deployment, limit int) ([]RestoredCopy, error) {
	rows, err := protectionsql.New(s.db.SQL()).RestoredCopiesOf(ctx, protectionsql.RestoredCopiesOfParams{SourceID: dep.ID, RowLimit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := []RestoredCopy{}
	for _, r := range rows {
		c, err := s.restoredCopyRecord(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// LastRestoreTest is a deployment's last restore test that got as far as
// an answer or an end, nil when it was never tested on its platform. A
// rehearsal never stands in for it.
func (s *Service) LastRestoreTest(ctx context.Context, dep *deployment.Deployment) (*RestoredCopy, error) {
	r, err := protectionsql.New(s.db.SQL()).LatestRestoreTest(ctx, dep.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.restoredCopyRecord(ctx, r)
}

// RestoredCopyRequest is what a person asks for: a copy (0: the newest
// good one in the store) and how long it stays open (0: a restore test,
// taken down as soon as it is checked).
type RestoredCopyRequest struct {
	Backup  ids.ID `json:"backup"`
	Minutes int64  `json:"minutes"`
	Test    bool   `json:"test"`
}

// restoredPlan is what a restored copy starts from.
type restoredPlan struct {
	dep     *deployment.Deployment
	target  *deployment.Target
	set     *Backup
	release *deployment.Release
	bundle  *deployment.ReleaseBundle
}

// planRestoredCopy resolves what a restored copy of a deployment starts
// from, refusing with why before anything is made.
func (s *Service) planRestoredCopy(ctx context.Context, projectID ids.ID, name string, set ids.ID) (*restoredPlan, error) {
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if err != nil || !dep.Standing() {
		return nil, fault.New(fault.NotFound, fmt.Sprintf("no standing deployment named %q", name))
	}
	p := &restoredPlan{dep: dep}
	if p.target, err = s.targets.ByID(ctx, dep.TargetID); err != nil {
		return nil, fmt.Errorf("%w: %w", deployment.ErrNoTarget, err)
	}
	// The platform is decided before this forge's own setup: configuring a
	// gate does not make a restored copy possible where none runs.
	if p.target.HoldsBackups() == nil {
		return nil, ErrRestoreTestAppPlatform
	}
	// The gate is placed by the provider's renderer; App Platform is the one
	// that can (an internal service there has no public route).
	if p.target.DataAccess() != deployment.DataOnManagedPlatform || p.target.NeedsMode != "managed" {
		return nil, fmt.Errorf("%w: %s is not an App Platform target that makes its databases; a restored copy runs where its source runs, behind a gate only App Platform places so far", ErrRestoredCopy, p.target.Name)
	}
	if s.restored.GateImage == "" || s.restored.Forge == "" {
		return nil, ErrNoGate
	}
	if s.restored.Secrets == nil {
		return nil, errors.New("restored copies are not configured")
	}
	// The copy is filled on the target's copy runner: refused here, before
	// anything is made, rather than after the place is up.
	if p.target.CopyRunner() == "" {
		return nil, deployment.ErrNoCopyRunner
	}
	if set.IsZero() {
		if p.set, err = s.newestStoreSet(ctx, projectID, name); err != nil {
			return nil, err
		}
	} else {
		if p.set, err = s.startSet(ctx, set); err != nil {
			return nil, err
		}
		if p.set.ProjectID != projectID || p.set.Deployment != name {
			return nil, fmt.Errorf("%w: set %d is a copy of another deployment", ErrRestoredCopy, set)
		}
	}
	// The newest set too: a restored copy fills managed databases only.
	if err := managedFillable(p.set, ErrRestoredCopy); err != nil {
		return nil, err
	}
	if p.set.DataClass == "production" && dep.DataClass != "production" {
		return nil, deployment.ErrProductionCopy
	}
	number, err := s.releases.LatestReleasedNumber(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if number == 0 {
		return nil, fmt.Errorf("%w: %s has served no release", ErrRestoredCopy, name)
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
		return nil, fmt.Errorf("%w: release %d was not published to a registry, so it cannot be run again by its images", ErrRestoredCopy, number)
	}
	// The release's bundle under the target's rules (its platform, and App
	// Platform's one image per repository), when the copy is asked for and
	// again when it starts, before anything is recorded or asked of the
	// provider.
	if err := s.providers.RefusesBundle(ctx, p.bundle, p.target); err != nil {
		return nil, err
	}
	return p, nil
}

// lifetimeOf is how long a request keeps the copy open; 0 is a test.
func lifetimeOf(in RestoredCopyRequest) (time.Duration, error) {
	if in.Test {
		return 0, nil
	}
	d := time.Duration(in.Minutes) * time.Minute
	switch {
	case in.Minutes == 0:
		return RestoredCopyDefault, nil
	case d < 15*time.Minute || d > RestoredCopyMost:
		return 0, fmt.Errorf("%w: a restored copy stays open between 15 minutes and %s", ErrRestoredCopy, RestoredCopyMost)
	}
	return d, nil
}

// RequestRestoredCopy starts a restored copy of a standing production
// deployment: a restore test, or a copy people can open for a while. It
// spends money and puts production data somewhere new, so it needs
// deployment:restore in the project; no agent carries it.
func (s *Service) RequestRestoredCopy(ctx context.Context, projectID ids.ID, name string, in RestoredCopyRequest) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentRestore, projectID, "deployment.restored-copy")
	if err != nil {
		return nil, err
	}
	life, err := lifetimeOf(in)
	if err != nil {
		return nil, err
	}
	if err := project.RefuseUnlessActive(ctx, s.db, projectID); err != nil {
		return nil, err
	}
	p, err := s.planRestoredCopy(ctx, projectID, name, in.Backup)
	if err != nil {
		return nil, err
	}
	return s.admit(ctx, p.dep, OperationRestore, actor, []string{deployment.DataHold(p.dep)}, map[string]any{"backup": p.set.ID, "minutes": int64(life / time.Minute), "test": in.Test}, nil)
}

// newGateSecret is 32 random bytes, as the gate reads them (64 hex).
func newGateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// restoredCopyName is a copy's deployment name: under the project's slug it
// is also its app's name on the platform, so it names the copy by its number.
func restoredCopyName(number int64) string { return fmt.Sprintf("copy-%d", number) }

func (s *Service) runRestore(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	set := inputID(t.op.Input, "backup")
	minutes, _ := t.op.Input["minutes"].(float64)
	test, _ := t.op.Input["test"].(bool)
	result := map[string]any{"backup": set}
	life := time.Duration(minutes) * time.Minute

	t.begin(ctx, "choosing the copy and the release")
	p, err := s.planRestoredCopy(ctx, t.op.ProjectID, t.op.Deployment, set)
	if err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}
	t.end(ctx, fmt.Sprintf("set %d, release %d, on %s", p.set.ID, p.release.Number, p.target.Name), 0)
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}

	q := protectionsql.New(s.db.SQL())
	// The copy's own mark is written before anything is made, so a
	// take-down can tell its app from another of the same name.
	marker, err := newGateSecret()
	if err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}
	asker := t.op.Authority.Actor()
	row, err := q.InsertRestoredCopy(ctx, protectionsql.InsertRestoredCopyParams{ID: ids.New(s.now()), ProjectID: p.dep.ProjectID, Source: p.dep.Name, SourceID: p.dep.ID,
		OperationID: t.op.ID, BackupID: p.set.ID, IsTest: test, LifetimeMinutes: int64(minutes), AskedBy: t.op.AskedBy, AskedByPrincipal: asker.Principal, AskedByVia: asker.Via, AppMarker: marker, At: s.now()})
	if err != nil {
		return OperationFailed, "nothing was made: recording the copy: " + err.Error(), result
	}
	result["copy"] = row.ID
	if err := t.record(ctx, "copy", row.ID); err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}
	secret, err := newGateSecret()
	if err != nil {
		return OperationFailed, "nothing was made: " + err.Error(), result
	}

	t.begin(ctx, "making the place")
	placed := s.now()
	expires := placed.UTC().Add(life)
	dep, err := s.deployments.PlaceRestoredCopy(ctx, deployment.RestoredPlacement{ProjectID: p.dep.ProjectID,
		Name: restoredCopyName(row.Number), TargetID: p.target.ID, DataClass: p.dep.DataClass, ExpiresAt: expires, BackupID: p.set.ID, At: placed})
	if err != nil {
		s.endRestoredCopy(ctx, row.ID, "nothing was made: "+err.Error())
		return OperationFailed, "nothing was made: making its deployment record: " + err.Error(), result
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		n, err := protectionsql.New(tx.SQL()).PlaceRestoredCopy(ctx, protectionsql.PlaceRestoredCopyParams{ID: row.ID, Deployment: dep.Name, DeploymentID: dep.ID, ReleaseNumber: p.release.Number,
			Target: p.target.Name, ExpiresAt: sql.NullTime{Time: expires, Valid: true}, SecretSha256: digestOf(secret)})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("the copy's record changed")
		}
		return audit.At(placed).RecordIn(ctx, tx, t.op.AskedBy, p.dep.ProjectID, "deployment.restored-copy-started", p.dep.Name, map[string]any{
			"copy": row.ID, "deployment": dep.Name, "backup": p.set.ID, "release": p.release.Number, "target": p.target.Name, "test": test, "expires": expires})
	})
	if err != nil {
		return s.restoreFailed(ctx, t, row.ID, dep, "making the place", err, result)
	}
	t.end(ctx, dep.Name+" on "+p.target.Name, 0)

	facts := RehearsalFacts{Egress: egressNotBlocked, Withheld: []string{}, NotStarted: []string{}, Checks: []RehearsalCheck{}, Images: p.bundle.Parts}
	gate := &deploy.Gate{Image: s.restored.GateImage, Secret: secret, Forge: s.restored.Forge, Marker: marker}
	url, contract, step, err := s.bootRestoredCopy(ctx, t, p, dep, gate, &facts)
	if err != nil {
		if rerr := s.recordRestoredCheck(ctx, row.ID, RehearsalFailed, step, facts, nil); rerr != nil {
			err = fmt.Errorf("%w (and the failed check could not be recorded on the copy: %v)", err, rerr)
		}
		return s.restoreFailed(ctx, t, row.ID, dep, step, err, result)
	}
	if _, err := q.RecordRestoredCopyURL(ctx, protectionsql.RecordRestoredCopyURLParams{ID: row.ID, GateUrl: url}); err != nil {
		return s.restoreFailed(ctx, t, row.ID, dep, "recording its address", err, result)
	}

	step = "asking the app through the gate"
	t.begin(ctx, step)
	asked := s.askThroughGate(ctx, url, secret, contract, &facts)
	answers := checkAnswers(facts)
	if asked {
		t.end(ctx, answers, 0)
	} else {
		t.fail(ctx, "not every check passed: "+answers)
	}
	held := s.checkGate(ctx, t, url, row.ID, contract, &facts)
	passed := asked && held
	outcome := RehearsalFailed
	if passed {
		outcome = RehearsalPassed
		if !slices.ContainsFunc(facts.Checks, func(c RehearsalCheck) bool { return strings.HasPrefix(c.Name, "data ") }) {
			outcome = RehearsalPassedNoData
		}
	}
	answers += "; " + gateAnswers(facts)
	failedStep := ""
	switch {
	case !asked:
		failedStep = step
	case !held:
		failedStep = gateCheckStep
	}
	if err := s.recordRestoredCheck(ctx, row.ID, outcome, failedStep, facts, func(tx *database.Tx) error {
		return audit.At(s.now()).RecordIn(ctx, tx, t.op.AskedBy, p.dep.ProjectID, "deployment.restore-tested", p.dep.Name, map[string]any{
			"copy": row.ID, "operation": t.op.ID, "outcome": outcome, "test": test, "backup": p.set.ID, "backupDigests": setDigests(p.set),
			"release": p.release.Number, "images": facts.Images, "target": p.target.Name, "checks": facts.Checks, "gate": facts.Gate,
			"withheld": facts.Withheld, "notStarted": facts.NotStarted, "egress": facts.Egress})
	}); err != nil {
		return s.restoreFailed(ctx, t, row.ID, dep, "recording the check", err, result)
	}
	result["outcome"] = outcome

	// A test, a copy that is not ready, or one whose gate failed its check
	// (it may let anyone in, or nobody) is taken down now; a copy that
	// answers stays open for people to look, even when a data check failed
	// (they may be looking for exactly that).
	if test || !readinessPassed(facts) || !held {
		left := s.takeDownRestored(ctx, t, row.ID, dep, nil)
		detail := fmt.Sprintf("release %d on set %d's data on %s answered %s", p.release.Number, p.set.ID, p.target.Name, answers)
		state := OperationVerified
		if !passed {
			state, detail = OperationFailed, "the restored copy did not pass: "+detail
		}
		if left != "" {
			state, detail = OperationFailed, detail+"; "+left
		}
		return state, detail, result
	}
	detail := fmt.Sprintf("open until %s: release %d on set %d's data on %s answered %s", expires.Format("15:04 MST 2 Jan"), p.release.Number, p.set.ID, p.target.Name, answers)
	if !passed {
		return OperationNotVerified, detail + "; a data check failed", result
	}
	return OperationVerified, detail, result
}

func checkAnswers(f RehearsalFacts) string {
	var answers []string
	for _, ch := range f.Checks {
		answers = append(answers, fmt.Sprintf("%s %d", ch.Path, ch.Status))
	}
	return strings.Join(answers, ", ")
}

// recordRestoredCheck writes a copy's check, and its receipt when given,
// together.
func (s *Service) recordRestoredCheck(ctx context.Context, id ids.ID, outcome, failedStep string, facts RehearsalFacts, receipt func(*database.Tx) error) error {
	raw, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := protectionsql.New(tx.SQL()).RecordRestoredCopyCheck(ctx, protectionsql.RecordRestoredCopyCheckParams{ID: id, Outcome: outcome, FailedStep: failedStep, FactsJson: raw, At: s.now()}); err != nil {
			return err
		}
		if receipt == nil {
			return nil
		}
		return receipt(tx)
	})
}

// restoreFailed ends a start that failed after its place was recorded:
// what it made is taken down at once, and the failure is said in safe
// words (the step, never output).
func (s *Service) restoreFailed(ctx context.Context, t *tracker, copyID ids.ID, dep *deployment.Deployment, step string, err error, result map[string]any) (runEnd, string, map[string]any) {
	why := safeWords(err)
	t.fail(ctx, why)
	detail := fmt.Sprintf("the restored copy failed while %s: %s", step, why)
	if left := s.takeDownRestored(ctx, t, copyID, dep, nil); left != "" {
		detail += "; " + left
	}
	return OperationFailed, detail, result
}

// safeWords is a restored copy's failure as it may be shown: a safe
// failure's own words, else the error, which on this path comes from the
// provider's API, the store or the forge (fill's command output is made
// safe where it is made, fillStartFrom).
func safeWords(err error) string {
	var safe *safeFailure
	if errors.As(err, &safe) {
		return safe.text
	}
	return err.Error()
}

// bootRestoredCopy makes the copy's databases, fills them from the set,
// renders the gated app and deploys it through the ordinary attempt (so its
// identity on the platform is recorded before anything else). It returns
// the gate's address and the contract it ran.
func (s *Service) bootRestoredCopy(ctx context.Context, t *tracker, p *restoredPlan, dep *deployment.Deployment, gate *deploy.Gate, facts *RehearsalFacts) (string, *appcontract.Contract, string, error) {
	step := "reading the release's source"
	t.begin(ctx, step)
	if s.sources == nil {
		return "", nil, step, errors.New("release sources are not configured")
	}
	project, err := s.sources.Project(ctx, dep.ProjectID)
	if err != nil {
		return "", nil, step, err
	}
	c, revision, err := s.sources.Contract(ctx, project, p.bundle.ContractRevision)
	if err != nil || c == nil {
		return "", nil, step, &safeFailure{"the release's .apptown/app.yaml could not be read"}
	}
	if why, err := s.env.RefusesContract(ctx, p.target, c); err != nil || why != "" {
		return "", nil, step, errors.Join(err, refusal(why))
	}
	if why, err := s.env.RefusesBrowserValues(ctx, p.target.Kind, dep.ProjectID, c); err != nil || why != "" {
		return "", nil, step, errors.Join(err, refusal(why))
	}
	for _, part := range c.Parts {
		if !part.Public {
			facts.NotStarted = append(facts.NotStarted, part.Name)
		}
	}
	tree, cleanup, err := (deploy.SelectedSource{Repository: project.Repository, Revision: revision}).Stage(ctx, "")
	if err != nil {
		return "", nil, step, &safeFailure{"the release's source could not be staged"}
	}
	defer cleanup()
	// The copy is a deployment of its own: it runs under its own profile
	// (default until one is chosen for it), recorded with its generation.
	c, applied, err := s.env.Profiled(ctx, dep.ProjectID, dep.Name, tree.Directory, c)
	if err != nil {
		return "", nil, step, err
	}
	secrets, withheld, err := s.restored.Secrets.ResolveRestoredCopy(ctx, project.Application, p.dep.Name, tree.Directory, c)
	facts.Withheld = append(facts.Withheld, withheld...)
	if err != nil {
		return "", nil, step, err
	}
	// Needs fulfilled by registering (an AI models key) are integrations:
	// a restored copy is not given them.
	for _, need := range c.Needs {
		if registered, err := s.env.FulfilledByRegistering(ctx, need); err == nil && registered {
			facts.Withheld = append(facts.Withheld, need)
		}
	}
	t.end(ctx, "", 0)

	d, err := s.providers.Driver(ctx, p.target, dep)
	if err != nil {
		return "", c, "starting it", err
	}
	src := deploy.Source{Revision: revision, Slug: project.Slug, Contract: c, Images: p.bundle.Parts, Init: p.bundle.Init, SelfServing: p.bundle.BuiltFrom.SelfServing, Workspace: tree.Directory}
	// Each line the target and the fill log is a step of the operation:
	// they are the platform's and the forge's own words.
	logf := func(format string, args ...any) { t.begin(ctx, fmt.Sprintf(format, args...)) }
	if pf, ok := d.(deploy.Preflighter); ok {
		if err := pf.Preflight(ctx, dep.Placement(), src, secrets, logf); err != nil {
			return "", c, "checking the target's room", err
		}
	}
	// A target that rounds resources to its own sizes answers before any
	// database is made.
	if applied, err = applied.Fitted(ctx, d, c); err != nil {
		return "", c, "fitting its parts to the target's sizes", err
	}
	if err := t.allowed(ctx); err != nil {
		return "", c, "making its databases", err
	}
	bindings, err := s.providers.PrepareNeeds(ctx, d, p.target, dep, src.Contract, t.op.AskedBy, logf)
	if err != nil {
		return "", c, "making its databases", err
	}
	current, err := s.deployments.ByID(ctx, dep.ID)
	if err != nil {
		return "", c, "filling its data", err
	}
	if err := t.allowed(ctx); err != nil {
		return "", c, "filling its data from the copy", err
	}
	receipt, err := s.starter.FillStart(ctx, deployment.StartFill{Deployment: current, Target: p.target, Logf: logf, Safe: true, Allowed: t.allowed})
	if err == nil {
		err = s.deployments.RecordStartedFrom(ctx, current, receipt, t.op.AskedBy, map[string]any{"restoredCopy": dep.Name})
	}
	if err != nil {
		return "", c, "filling its data from the copy", err
	}
	placement := dep.Placement()
	placement.Gate = gate
	r, ok := d.(deploy.Renderer)
	if !ok {
		return "", c, "rendering", fmt.Errorf("%s renders no artifact of its own", p.target.Kind)
	}
	art, err := r.Render(ctx, placement, src, bindings, secrets, logf)
	if err != nil {
		return "", c, "rendering the gated app", err
	}
	art.Source = &src
	key := fmt.Sprintf("restored-copy-%d", dep.ID)
	inputs := deployment.Inputs{BundleID: p.bundle.ID, Revision: revision, ArtifactHash: art.Hash, ImageDigest: p.bundle.Parts[c.PublicPart().Name], Applied: applied}
	base := dep.DesiredGeneration
	if err := t.allowed(ctx); err != nil {
		return "", c, "deploying it", err
	}
	res, err := (deployment.Runner{Store: s.outcomes, Applied: s.applied}).Run(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: key, Inputs: inputs, BaseGeneration: &base},
		func(ctx context.Context, at deployment.Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			return d.Reconcile(ctx, placement, at.Generation, art, key, logf, checkpoint)
		})
	if err != nil {
		return "", c, "deploying it", err
	}
	if res.Outcome != deployment.Applied || res.Observed.URL == "" {
		return "", c, "deploying it", fmt.Errorf("the platform did not report it running (%s)", res.Outcome)
	}
	t.end(ctx, "", 0)
	return strings.TrimRight(res.Observed.URL, "/"), c, "", nil
}

// gateClient asks through the gate: it follows no redirect (a redirect's
// status is the answer, and the secret is never sent on to another
// address) and reads no body.
func (s *Service) gateClient() *http.Client {
	if s.restored.HTTP != nil {
		return s.restored.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// askThroughGate runs the rehearsal's checks against the copy through its
// gate, with the gate secret: readiness, the front page, then the declared
// data checks. Each is retried until the grace runs out, then judged once;
// only its status is kept, and the body is discarded unread.
func (s *Service) askThroughGate(ctx context.Context, url, secret string, c *appcontract.Contract, facts *RehearsalFacts) bool {
	grace := s.restored.Grace
	if grace <= 0 {
		grace = 3 * time.Minute
	}
	client := s.gateClient()
	get := func(path string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+path, nil)
		if err != nil {
			return 0
		}
		req.Header.Set(deploy.GateProbeHeader, secret)
		res, err := client.Do(req)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return res.StatusCode
	}
	ask := func(name, path string, judge func(int) bool) bool {
		status := 0
		ok := poll.Until(ctx, grace, 2*time.Second, func() bool { status = get(path); return judge(status) })
		facts.Checks = append(facts.Checks, RehearsalCheck{Name: name, Path: path, Status: status, Passed: ok})
		return ok
	}
	readiness, ready := readinessOf(c)
	passed := ask("readiness", readiness, ready)
	passed = ask("page", "/", func(n int) bool { return n != 0 && check.PageRefusal(n, "/") == "" }) && passed
	for _, line := range c.RehearsalChecks {
		path, expect, err := appcontract.ParseDataCheck(line)
		if err != nil {
			continue // the contract parser refused it already
		}
		passed = ask("data "+line, path, func(n int) bool { return n == expect }) && passed
	}
	return passed
}

// readinessOf is the copy's readiness question: the path its public part's
// cold-boot probe asks ("/" when it names none), and how its answer is
// judged.
func readinessOf(c *appcontract.Contract) (string, func(int) bool) {
	rp := docker.RenderColdBootProbe(c.PublicPart(), nil)
	path := rp.Path
	if path == "" {
		path = "/"
	}
	return path, func(n int) bool { return n != 0 && (rp.Expect == 0 && n < 400 || n == rp.Expect) }
}

// gateCheckStep is where a restore fails when its gate does not hold.
const gateCheckStep = "checking the gate"

// gateCheckFor is how long the session the forge's own ticket opens lasts:
// long enough for the one read the check makes with it.
const gateCheckFor = 2 * time.Minute

// checkGate is the forge checking the copy's gate itself, so a restore test
// proves the door as well as the app without a person signing in. At the
// gate's public address it asks the readiness path with no token and with
// a wrong one (each the gate's own empty 404, marked by the gate and not
// to be stored), and enters with a made-up ticket (the gate's 403). Then it issues a ticket to itself, as Open issues a person's, and
// enters with it once: the gate redeems it with the forge (RedeemGateTicket)
// and answers with a session. The same ticket is refused a second time, and
// the session is served the app's readiness path. The ticket lives only
// here and is used up by the check; the forge's redemption is its own
// receipt, never a person's entry. Each answer is a step and a fact (status
// codes only); it reports whether every one passed.
func (s *Service) checkGate(ctx context.Context, t *tracker, url string, copyID ids.ID, c *appcontract.Contract, facts *RehearsalFacts) bool {
	client := s.gateClient()
	// get answers the status, the headers and whether there was a body; the
	// body itself is read past and never kept.
	get := func(path, token, session string) (int, http.Header, bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+path, nil)
		if err != nil {
			return 0, http.Header{}, false
		}
		if token != "" {
			req.Header.Set(deploy.GateProbeHeader, token)
		}
		if session != "" {
			req.AddCookie(&http.Cookie{Name: gate.CookieName, Value: session})
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, http.Header{}, false
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return res.StatusCode, res.Header, n > 0
	}
	held := true
	check := func(name, path string, ask func() (int, bool, string)) {
		t.begin(ctx, gateCheckStep+": "+name)
		status, ok, said := ask()
		facts.Gate = append(facts.Gate, RehearsalCheck{Name: name, Path: path, Status: status, Passed: ok})
		detail := fmt.Sprintf("%s %d", path, status)
		if said != "" {
			detail += ", " + said
		}
		if ok {
			t.end(ctx, detail, 0)
		} else {
			t.fail(ctx, detail)
		}
		held = held && ok
	}
	noStore := func(h http.Header) bool { return strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-store") }
	// The gate's own refusal carries its mark, which it strips from what
	// the app sends: an app's 404 or 403 behind a gate that let the request
	// through never reads as a refusal.
	byGate := func(h http.Header) bool { return h.Get(gate.RefusedHeader) == gate.RefusedValue }
	// Refusals are asked on the readiness path, which answered the forge's
	// probe: a request let through would be answered there, not refused.
	readiness, ready := readinessOf(c)
	refused := func(token string) func() (int, bool, string) {
		return func() (int, bool, string) {
			status, h, body := get(readiness, token, "")
			switch {
			case status != http.StatusNotFound:
				return status, false, "wanted 404"
			case !byGate(h):
				return status, false, "not the gate's refusal"
			case body:
				return status, false, "not empty"
			case !noStore(h):
				return status, false, "without no-store, so it may be kept"
			}
			return status, true, "the gate's empty refusal, no-store"
		}
	}
	entryRefused := func(path string) (int, bool, string) {
		status, h, _ := get(path, "", "")
		switch {
		case status != http.StatusForbidden:
			return status, false, "wanted 403"
		case !byGate(h):
			return status, false, "not the gate's refusal"
		}
		return status, true, "the gate's refusal"
	}
	check("refuses a request with no token", readiness, refused(""))
	check("refuses a wrong token", readiness, func() (int, bool, string) {
		wrong, err := newGateSecret()
		if err != nil {
			return 0, false, err.Error()
		}
		return refused(wrong)()
	})
	check("refuses a made-up ticket", gate.EnterPath, func() (int, bool, string) {
		madeUp, err := newGateSecret()
		if err != nil {
			return 0, false, err.Error()
		}
		return entryRefused(gate.EnterPath + "?ticket=" + madeUp)
	})
	ticket, issued := s.issueTicket(ctx, copyID, operations.SystemActor, t.op.ID)
	session := ""
	check("lets AppTown's own ticket in", gate.EnterPath, func() (int, bool, string) {
		if issued != nil {
			return 0, false, "the ticket could not be issued: " + issued.Error()
		}
		status, h, _ := get(gate.EnterPath+"?ticket="+ticket, "", "")
		for _, ck := range (&http.Response{Header: h}).Cookies() {
			if ck.Name == gate.CookieName && ck.Value != "" {
				session = ck.Value
			}
		}
		if session == "" {
			return status, false, "no session"
		}
		return status, status == http.StatusSeeOther, "a session"
	})
	check("refuses that ticket a second time", gate.EnterPath, func() (int, bool, string) {
		if issued != nil {
			return 0, false, "no ticket was issued"
		}
		return entryRefused(gate.EnterPath + "?ticket=" + ticket)
	})
	check("serves the app to that session", readiness, func() (int, bool, string) {
		if session == "" {
			return 0, false, "no session to ask with"
		}
		status, _, _ := get(readiness, "", session)
		return status, ready(status), ""
	})
	return held
}

// gateAnswers is the gate check in a few words: how many held, and each
// that did not.
func gateAnswers(f RehearsalFacts) string {
	var failed []string
	for _, ch := range f.Gate {
		if !ch.Passed {
			failed = append(failed, fmt.Sprintf("%s (%s %d)", ch.Name, ch.Path, ch.Status))
		}
	}
	if len(failed) == 0 {
		return fmt.Sprintf("the gate passed its %d checks", len(f.Gate))
	}
	return "the gate failed: " + strings.Join(failed, ", ")
}

// takeDownRestored removes everything a restored copy made: its app and
// the clusters made for it, then its deployment record. No final copy is
// taken: its data is a copy of a copy. It returns what was left, "" when
// nothing was; the copy is marked ended only when nothing was left.
//
// What to delete is what the forge recorded making, and also what the
// platform holds under the names and markers the forge gives what it makes:
// a create the platform answered but the forge never wrote down
// (it failed or stopped in between) is still found and deleted. When that
// cannot be established (the lookup fails, or a need's intent was recorded
// and neither an identity nor a cluster of its name is found while the
// lookup could not say), the copy stays standing, its record and its
// source's protection kept, and the card says what is unresolved.
//
// allowed, when set, is asked before each removal: a person's take-down
// stops when their authority is withdrawn, and the copy stays standing for
// the sweep. A start cleaning up after itself passes nil: removing what the
// operation itself made is the forge acting for itself, as its sweep would.
func (s *Service) takeDownRestored(ctx context.Context, t *tracker, copyID ids.ID, dep *deployment.Deployment, allowed func(context.Context) error) string {
	if allowed == nil {
		allowed = func(context.Context) error { return nil }
	}
	ctx = context.WithoutCancel(ctx)
	t.begin(ctx, "taking the restored copy down")
	current, err := s.deployments.ByID(ctx, dep.ID)
	if found, lerr := database.Lookup(err); lerr != nil {
		return s.restoredLeft(ctx, t, copyID, "its record could not be read: "+lerr.Error())
	} else if !found {
		s.endRestoredCopy(ctx, copyID, "taken down")
		t.end(ctx, "already removed", 0)
		return ""
	}
	if current.Kind != deployment.Restored {
		return s.restoredLeft(ctx, t, copyID, current.Name+" is not a restored copy, so it was not taken down")
	}
	target, err := s.targets.ByID(ctx, current.TargetID)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "its target could not be read: "+err.Error())
	}
	d, err := s.providers.Driver(ctx, target, current)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, err.Error())
	}
	gated, ok := d.(gatedRemover)
	if !ok {
		return s.restoredLeft(ctx, t, copyID, fmt.Sprintf("%s cannot find or remove a gated app", target.Kind))
	}
	row, err := protectionsql.New(s.db.SQL()).RestoredCopyByID(ctx, copyID)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "its record could not be read: "+err.Error())
	}
	placement := current.Placement()
	placement.Gate.Marker = row.AppMarker
	made, err := gated.FindMade(ctx, placement)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "what it made could not be looked up on the platform, so whether anything stands is unknown: "+err.Error())
	}
	var deleted []string
	apps := map[string]bool{}
	recorded, err := s.restoredAppID(ctx, current)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "the app's recorded identity could not be read: "+err.Error())
	}
	for _, id := range []string{recorded, made.AppID} {
		if id == "" || apps[id] {
			continue
		}
		apps[id] = true
		if err := allowed(ctx); err != nil {
			return s.restoredLeft(ctx, t, copyID, fmt.Sprintf("the app %s was not removed: %v", id, err))
		}
		if err := gated.RemoveApp(ctx, id); err != nil {
			return s.restoredUncertain(ctx, t, copyID, fmt.Sprintf("the app %s", id), err)
		}
		deleted = append(deleted, "app "+id)
		if s.applied != nil {
			s.applied()
		}
	}
	items, err := s.managedList(ctx, current, target)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, err.Error())
	}
	source, err := s.restoredSourceInstances(ctx, copyID)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, err.Error())
	}
	md := s.providers.ManagedData(target)
	clusters := map[string]string{} // id -> name
	for _, it := range items {
		if it.Instance != "" {
			clusters[it.Instance] = it.Cluster
		}
	}
	for _, c := range made.Clusters {
		clusters[c.ID] = c.Name
	}
	for id, name := range clusters {
		// Only what was made for this copy goes: never an instance its
		// source holds, whatever the copy's record or a name says.
		if source[id] {
			return s.restoredLeft(ctx, t, copyID, fmt.Sprintf("the cluster %s is its source's, so it was not deleted", name))
		}
		if md == nil {
			return s.restoredLeft(ctx, t, copyID, "the target has no managed databases to delete "+name+" from")
		}
		if err := allowed(ctx); err != nil {
			return s.restoredLeft(ctx, t, copyID, fmt.Sprintf("the cluster %s was not deleted: %v", name, err))
		}
		if err := md.DeleteInstance(ctx, id); err != nil {
			return s.restoredUncertain(ctx, t, copyID, "the cluster "+name, err)
		}
		deleted = append(deleted, "cluster "+name+" ("+id+")")
	}
	// Its own bucket and the bucket's key go too: they hold a copy of
	// production objects. Only the copy's own list is read, so its source's
	// bucket is never touched; a restored copy's data is disposable, so no
	// final copy is taken.
	owned, err := s.deployments.Owned(ctx, current.ID)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "what it owns could not be read: "+err.Error())
	}
	if _, storage := splitStorage(owned); len(storage) > 0 {
		if err := allowed(ctx); err != nil {
			return s.restoredLeft(ctx, t, copyID, "its bucket and key were not deleted: "+err.Error())
		}
		bd, err := s.bucketData(ctx, target, current)
		if err == nil && bd == nil {
			err = fmt.Errorf("%s cannot delete a bucket", target.Kind)
		}
		if err != nil {
			return s.restoredLeft(ctx, t, copyID, "its bucket and key were not deleted: "+err.Error())
		}
		gone := bd.TearDownStorage(ctx, current.Placement(), deploy.TakeDown{Owned: storage, Synthetic: true})
		deleted = append(deleted, gone.Deleted...)
		if len(gone.Left) > 0 {
			var left []string
			for _, l := range gone.Left {
				name, _ := l.Handle["name"].(string)
				left = append(left, l.What+" "+name+" ("+l.Why+")")
			}
			return s.restoredLeft(ctx, t, copyID, "not removed: "+strings.Join(left, "; "))
		}
	}
	// Every need the copy recorded making is now gone: deleted by its
	// identity or by its name, or never made (the platform holds nothing of
	// that name, and the lookup answered).
	gone := map[string]bool{}
	insts, err := s.needs.ForDeployment(ctx, current.ID)
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "its databases' records could not be read: "+err.Error())
	}
	for _, inst := range insts {
		gone[inst.Need] = true
	}
	var retained []string
	if err := allowed(ctx); err != nil {
		return s.restoredLeft(ctx, t, copyID, "deleted "+strings.Join(deleted, ", ")+", but its record was not removed: "+err.Error())
	}
	_, err = database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		retained, err = deployment.RemoveRestoredIn(ctx, tx, current.ID, deployment.RemovalSettlement{Target: target.Name, By: t.op.Authority, Taken: deploy.TakenDown{Deleted: deleted}, Deleted: gone, At: s.now()})
		if err == nil && len(retained) > 0 {
			err = fmt.Errorf("it would keep %s as retained", strings.Join(retained, ", "))
		}
		return err == nil, err
	})
	if err != nil {
		return s.restoredLeft(ctx, t, copyID, "deleted "+strings.Join(deleted, ", ")+", but its record was not removed: "+err.Error())
	}
	detail := "taken down; the platform held nothing of it"
	if len(deleted) > 0 {
		detail = "taken down: deleted " + strings.Join(deleted, ", ")
	}
	if len(made.NotOurs) > 0 {
		detail += "; found and left alone, as not this copy's: " + strings.Join(made.NotOurs, ", ")
	}
	s.endRestoredCopy(ctx, copyID, detail)
	t.end(ctx, detail, 0)
	return ""
}

// gatedRemover is what a provider driver offers to take a gated
// deployment down: remove its app by identity, and find what its start
// made by name when an identity was not recorded.
type gatedRemover interface {
	RemoveApp(ctx context.Context, appID string) error
	FindMade(ctx context.Context, p deploy.Placement) (digitalocean.Made, error)
}

// restoredAppID is the app the copy's attempt recorded creating: the
// deployment's settled handle, else the attempt's checkpoint, which is
// written as soon as the platform names the app.
func (s *Service) restoredAppID(ctx context.Context, dep *deployment.Deployment) (string, error) {
	if id, _ := dep.Handle["appId"].(string); id != "" {
		return id, nil
	}
	at, _, err := s.outcomes.AttemptByKey(ctx, dep.ID, fmt.Sprintf("restored-copy-%d", dep.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, _, noted, err := s.outcomes.RecoveryAttempt(ctx, at.ID)
	if err != nil {
		return "", err
	}
	id, _ := noted["appId"].(string)
	return id, nil
}

// restoredSourceInstances are the instances the copy's source holds now.
func (s *Service) restoredSourceInstances(ctx context.Context, copyID ids.ID) (map[string]bool, error) {
	r, err := protectionsql.New(s.db.SQL()).RestoredCopyByID(ctx, copyID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	insts, err := s.needs.ForDeployment(ctx, r.SourceID)
	if err != nil {
		return nil, err
	}
	for _, inst := range insts {
		if id, _ := inst.Handle["id"].(string); id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// restoredUncertain is restoredLeft for a removal the provider refused, or
// one it gave no answer to, so whether it happened is not known.
func (s *Service) restoredUncertain(ctx context.Context, t *tracker, copyID ids.ID, what string, err error) string {
	if !deploy.Refused(err) {
		t.indeterminate = true
		left := fmt.Sprintf("removing %s got no answer AppTown can trust, so whether it still stands is not known; AppTown tries again: %v", what, err)
		t.log.Unknown(ctx, left)
		s.noteRestoredLeft(ctx, copyID, left)
		return left
	}
	return s.restoredLeft(ctx, t, copyID, fmt.Sprintf("%s: %v", what, err))
}

// restoredLeft records what a take-down could not remove; the copy stays
// standing on its card, and the sweep tries again.
func (s *Service) restoredLeft(ctx context.Context, t *tracker, copyID ids.ID, why string) string {
	left := "left standing, so AppTown tries again: " + why
	t.fail(ctx, left)
	s.noteRestoredLeft(ctx, copyID, left)
	return left
}

// noteRestoredLeft shows on the copy's card why it still stands. The
// operation's own record says the same and the sweep acts on the copy, not
// the note, so a failure loses only the card's words.
func (s *Service) noteRestoredLeft(ctx context.Context, copyID ids.ID, left string) {
	if _, err := protectionsql.New(s.db.SQL()).NoteRestoredCopyLeft(ctx, protectionsql.NoteRestoredCopyLeftParams{ID: copyID, EndDetail: left}); err != nil {
		log.Printf("restored copy %s: noting what was left: %v", copyID, err)
	}
}

// endRestoredCopy records that a copy is gone. A failure leaves it shown as
// standing; the sweep finds nothing to remove and ends it again.
func (s *Service) endRestoredCopy(ctx context.Context, copyID ids.ID, detail string) {
	if _, err := protectionsql.New(s.db.SQL()).EndRestoredCopy(context.WithoutCancel(ctx), protectionsql.EndRestoredCopyParams{ID: copyID, EndDetail: detail, At: s.now()}); err != nil {
		log.Printf("restored copy %s: recording its end: %v", copyID, err)
	}
}

// restoredCopyIn is a project's restored copy by id.
func (s *Service) restoredCopyIn(ctx context.Context, projectID ids.ID, id ids.ID) (protectionsql.RestoredCopy, error) {
	r, err := protectionsql.New(s.db.SQL()).RestoredCopyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.ProjectID != projectID) {
		return r, fault.New(fault.NotFound, fmt.Sprintf("no restored copy %d", id))
	}
	return r, err
}

// TakeDownRestoredCopy takes a restored copy down now, as an operation on
// the copy. It needs deployment:restore in the project, as starting it did.
func (s *Service) TakeDownRestoredCopy(ctx context.Context, projectID ids.ID, id ids.ID) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentRestore, projectID, "deployment.restored-copy.take-down")
	if err != nil {
		return nil, err
	}
	r, err := s.restoredCopyIn(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if r.EndedAt.Valid {
		return nil, fmt.Errorf("%w: copy %d was taken down at %s", ErrRestoredCopy, id, r.EndedAt.Time.Format(time.RFC3339))
	}
	dep, err := s.deployments.ByID(ctx, r.DeploymentID)
	if err != nil || dep.Kind != deployment.Restored {
		return nil, fmt.Errorf("%w: copy %d is still starting; it is taken down when its start ends", ErrRestoredCopy, id)
	}
	return s.admit(ctx, dep, OperationRestoredCopyEnd, actor, []string{deployment.SubjectOf(dep)}, map[string]any{"copy": id}, nil)
}

// runRestoredTakeDown takes a restored copy down: no final copy, its data
// is a copy of a copy.
func (s *Service) runRestoredTakeDown(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	dep, err := s.deployments.ByName(ctx, t.op.ProjectID, t.op.Deployment)
	if err != nil {
		return OperationFailed, "the restored copy's deployment could not be read: " + err.Error(), nil
	}
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	r, err := protectionsql.New(s.db.SQL()).RestoredCopyByDeployment(ctx, dep.ID)
	if err != nil {
		return OperationFailed, "the restored copy's record could not be read: " + err.Error(), nil
	}
	if left := s.takeDownRestored(ctx, t, r.ID, dep, t.allowed); left != "" {
		return OperationFailed, left, map[string]any{"copy": r.ID}
	}
	return OperationDone, "taken down; no final copy was taken (its data is a copy of a copy)", map[string]any{"copy": r.ID}
}

// ExtendRestoredCopy keeps an open restored copy open until now plus the
// given minutes. It needs deployment:restore, as starting it did; the
// receipt names who extended it and until when.
func (s *Service) ExtendRestoredCopy(ctx context.Context, projectID ids.ID, id ids.ID, minutes int64) (*RestoredCopy, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentRestore, projectID, "deployment.restored-copy.extend")
	if err != nil {
		return nil, err
	}
	life, err := lifetimeOf(RestoredCopyRequest{Minutes: minutes})
	if err != nil {
		return nil, err
	}
	r, err := s.restoredCopyIn(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	at := s.now()
	until := at.UTC().Add(life)
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := protectionsql.New(tx.SQL())
		n, err := q.ExtendRestoredCopy(ctx, protectionsql.ExtendRestoredCopyParams{ID: id, ExpiresAt: sql.NullTime{Time: until, Valid: true}})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: copy %d is a test or was taken down", ErrRestoredCopy, id)
		}
		if err := deployment.ExtendRestoredCopyIn(ctx, tx, r.DeploymentID, until); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, actor.Name, projectID, "deployment.restored-copy-extended", r.Source, map[string]any{"copy": id, "deployment": r.Deployment, "until": until})
	})
	if err != nil {
		return nil, err
	}
	r, err = s.restoredCopyIn(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	return s.restoredCopyRecord(ctx, r)
}

// OpenRestoredCopy issues a one-use ticket for a person to enter an open
// restored copy, and the address that redeems it at the copy's gate. Only
// a person signed in to the forge in a browser, who holds
// deployment:open-restored in the project, gets one; the ticket is kept
// as its SHA-256, is bound to that person and that copy, and lasts a
// minute. Issuing it is not an entry: the entry is witnessed when the gate
// redeems it (RedeemGateTicket).
func (s *Service) OpenRestoredCopy(ctx context.Context, projectID ids.ID, id ids.ID) (string, error) {
	if s.authority == nil {
		return "", errors.New("restored copies are not configured")
	}
	caller, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentOpenRestored, Resource: projectResource(projectID),
		Context: access.Context{Route: "deployment.restored-copy.open", Consequential: true}})
	if err != nil {
		return "", err
	}
	if caller.Principal.Name == "" || access.AgentCredential(caller.Principal) || (caller.Principal.Via != "session" && caller.Principal.Via != "proxy") {
		return "", ErrBrowserOnly
	}
	r, err := s.restoredCopyIn(ctx, projectID, id)
	if err != nil {
		return "", err
	}
	c, err := s.restoredCopyRecord(ctx, r)
	if err != nil {
		return "", err
	}
	if !c.Open {
		return "", fmt.Errorf("%w: copy %d is not open (a test, not ready, out of time or taken down)", ErrRestoredCopy, id)
	}
	ticket, err := s.issueTicket(ctx, id, caller.Principal.Name, ids.Nil)
	if err != nil {
		return "", err
	}
	return r.GateUrl + gate.EnterPath + "?ticket=" + ticket, nil
}

// issueTicket records a one-use ticket to a copy's gate for holder, kept
// by its SHA-256 and lasting a minute (TicketFor), and answers it. checkOp
// is 0 for a person's Open, or the copy's start operation when the ticket
// is the forge's own gate check (checkGate).
func (s *Service) issueTicket(ctx context.Context, copyID ids.ID, holder string, checkOp ids.ID) (string, error) {
	ticket, err := newGateSecret()
	if err != nil {
		return "", err
	}
	now := s.now()
	lasts := s.restored.TicketFor
	if lasts <= 0 {
		lasts = time.Minute
	}
	person, err := identity.ActorIn(ctx, s.db, holder)
	if err != nil {
		return "", err
	}
	if err := protectionsql.New(s.db.SQL()).InsertGateTicket(ctx, protectionsql.InsertGateTicketParams{TicketSha256: digestOf(ticket), CopyID: copyID,
		Person: holder, PersonPrincipal: person.Principal, PersonVia: person.Via, ExpiresAt: now.Add(lasts), CheckOperation: checkOp, IssuedAt: now}); err != nil {
		return "", err
	}
	return ticket, nil
}

// GateEntryGrant is what a redeemed ticket lets the gate do: admit this
// person until then.
type GateEntryGrant struct {
	Person string    `json:"person"`
	Until  time.Time `json:"until"`
}

// RedeemGateTicket is a gate redeeming a person's ticket. The gate proves
// it is a standing copy's by its secret, and the ticket must be that
// copy's, unused and within its minute. The person's grant is checked
// again now; then the ticket is used and the entry's receipt written in
// one transaction, so of two redemptions at once at most one succeeds and
// every entry has its receipt. The grant lasts until the copy's expiry or
// an hour, whichever is sooner. The receipt says the gate let this person
// in; it does not say they signed in to the app, or what they read.
//
// A ticket the forge issued itself for checkGate is redeemed by the same
// rule, and lets in only that check: see redeemCheckTicket.
func (s *Service) RedeemGateTicket(ctx context.Context, secret, ticket string) (GateEntryGrant, error) {
	if secret == "" || ticket == "" {
		return GateEntryGrant{}, ErrGateCredential
	}
	q := protectionsql.New(s.db.SQL())
	r, err := q.RestoredCopyByGateSecret(ctx, digestOf(secret))
	if errors.Is(err, sql.ErrNoRows) {
		return GateEntryGrant{}, ErrGateCredential
	}
	if err != nil {
		return GateEntryGrant{}, err
	}
	c, err := s.restoredCopyRecord(ctx, r)
	if err != nil {
		return GateEntryGrant{}, err
	}
	var grant GateEntryGrant
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		at := s.now()
		used, err := protectionsql.New(tx.SQL()).RedeemGateTicket(ctx, protectionsql.RedeemGateTicketParams{TicketSha256: digestOf(ticket), CopyID: r.ID, At: at})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTicket
		}
		if err != nil {
			return err
		}
		if !used.CheckOperation.IsZero() {
			grant, err = s.redeemCheckTicket(ctx, tx, r, used.CheckOperation, at)
			return err
		}
		// A person enters only an open copy.
		if !c.Open {
			return ErrTicket
		}
		person := used.Person
		// The person may have lost the grant since Open: ask again, as the
		// principal the ticket was issued to, whatever their name is now; a
		// ticket naming no principal lets nobody in. The ticket stays used
		// either way.
		if used.PersonPrincipal.IsZero() {
			return ErrTicket
		}
		as := access.WithCaller(ctx, access.Caller{Principal: access.Principal{ID: used.PersonPrincipal, Kind: "user", Name: person, Via: "gate-ticket"}, Verified: true, Receipt: person})
		if _, err := s.authority.Require(as, access.Permission{Capability: access.CapDeploymentOpenRestored, Resource: projectResource(r.ProjectID),
			Context: access.Context{Route: "deployment.restored-copy.enter", Consequential: true}}); err != nil {
			return err
		}
		until := at.UTC().Add(GateSessionMost)
		if r.ExpiresAt.Time.Before(until) {
			until = r.ExpiresAt.Time.UTC()
		}
		grant = GateEntryGrant{Person: person, Until: until}
		return audit.At(at).RecordIn(as, tx, person, r.ProjectID, "deployment.gate-entered", r.Source, map[string]any{
			"copy": r.ID, "deployment": r.Deployment, "backup": r.BackupID, "until": until,
			"meaning": "AppTown gate admitted this person to the restored copy; it does not record signing in to the app or what was read"})
	})
	return grant, err
}

// redeemCheckTicket lets in the forge's own gate check, and nothing else:
// the ticket must have been issued by the copy's own start, and that start
// must still be running. Nobody's grant is asked, because no person enters;
// the session is the forge's, for a couple of minutes, and the receipt
// (deployment.gate-checked) names the forge, never a person, and is not an
// entry on the card.
func (s *Service) redeemCheckTicket(ctx context.Context, tx *database.Tx, r protectionsql.RestoredCopy, checkOp ids.ID, at time.Time) (GateEntryGrant, error) {
	if checkOp != r.OperationID || s.ops == nil {
		return GateEntryGrant{}, ErrTicket
	}
	start, err := s.ops.ByID(ctx, checkOp)
	if errors.Is(err, operations.ErrMissing) {
		return GateEntryGrant{}, ErrTicket
	}
	if err != nil {
		return GateEntryGrant{}, err
	}
	if !start.State.Live() {
		return GateEntryGrant{}, ErrTicket
	}
	grant := GateEntryGrant{Person: operations.SystemActor, Until: at.UTC().Add(gateCheckFor)}
	return grant, audit.At(at).RecordIn(ctx, tx, operations.SystemActor, r.ProjectID, "deployment.gate-checked", r.Source, map[string]any{
		"copy": r.ID, "deployment": r.Deployment, "operation": checkOp, "until": grant.Until,
		"meaning": "AppTown's own check of the restored copy's gate: a ticket its start issued to itself, redeemed once through the gate; nobody entered"})
}

// SweepRestoredCopies takes down every restored copy whose time is up, and
// every one a start left standing (a test, or a start the forge stopped in
// the middle of or that failed): each by a take-down operation of its own.
// A copy whose start made no deployment record made nothing, and is ended.
func (s *Service) SweepRestoredCopies(ctx context.Context) (started int, skipped []string, err error) {
	return s.sweepRestoredCopies(ctx, s.now())
}

func (s *Service) sweepRestoredCopies(ctx context.Context, now time.Time) (started int, skipped []string, err error) {
	if s.ops == nil {
		return 0, nil, nil
	}
	q := protectionsql.New(s.db.SQL())
	open, err := q.OpenRestoredCopies(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range open {
		// A copy is due its take-down when its start has ended and its time
		// is up, it was a test, or its start failed or is unknown.
		start, err := s.ops.ByID(ctx, r.OperationID)
		if errors.Is(err, operations.ErrMissing) {
			start = nil
		} else if err != nil {
			return started, skipped, err
		}
		if start != nil && start.State.Live() {
			continue
		}
		failed := start == nil || start.State == operations.Failed || start.State == operations.Unknown || start.State == operations.Cancelled
		if !(failed || r.IsTest || !r.ExpiresAt.Valid || !r.ExpiresAt.Time.After(now)) {
			continue
		}
		if r.DeploymentID.IsZero() {
			s.endRestoredCopy(ctx, r.ID, "nothing was made")
			continue
		}
		dep, err := s.deployments.ByID(ctx, r.DeploymentID)
		if found, lerr := database.Lookup(err); lerr != nil {
			return started, skipped, lerr
		} else if !found {
			s.endRestoredCopy(ctx, r.ID, "taken down")
			continue
		}
		if dep.Kind != deployment.Restored {
			skipped = append(skipped, fmt.Sprintf("copy %d: %s is not a restored copy", r.ID, dep.Name))
			continue
		}
		if _, err := s.admit(ctx, dep, OperationRestoredCopyEnd, operations.System("restored-copy-sweep"), []string{deployment.SubjectOf(dep)}, map[string]any{"copy": r.ID}, nil); err != nil {
			var running *operations.HeldError
			if errors.As(err, &running) {
				skipped = append(skipped, fmt.Sprintf("copy %d: %v", r.ID, err))
				continue
			}
			return started, skipped, err
		}
		started++
	}
	return started, skipped, nil
}

// refusal is a contract's refusal on the target, as Deployment refuses it.
func refusal(why string) error {
	if why == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", deployment.ErrRefusedContract, why)
}
