package dataprotection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// A release onto a deployment that already holds data is the moment that
// data is most at risk: on 21 Oct 2026 sixteen services were made again
// on empty storage by ordinary releases, with no copy taken first and
// nothing to say the data had gone. So a landing takes a copy before it
// releases (BeforeRelease) and is refused when that copy is incomplete,
// unless a person accepts; and it witnesses each need's data afterwards
// (After): the storage it is on and how much it holds, compared.

// TriggerPreRelease is the set a landing takes before it releases onto a
// deployment that runs a release already.
const TriggerPreRelease = "pre-release"

// ReleaseWitness is what a landing learned about a standing deployment's
// data before releasing onto it, and checks against afterwards.
type ReleaseWitness struct {
	// Copy is the set taken before the release; nil when none was needed,
	// and Passed says why (never released, holds no data, its target keeps
	// its own backups).
	Copy   *Backup
	Passed string
	// missing is why no copy could be taken of data the deployment names:
	// not a pass, and not complete.
	missing string
	// Notes are what was not witnessed, and why: shown, not failures.
	Notes []string

	synthetic bool
	dk        docker.Scope
	needs     []needFacts
}

// needFacts is one need service's data as read before the release.
type needFacts struct {
	need, container, path, measure string
	mount                          docker.DataMount
	created                        string           // the volume's creation time; "" for a bind mount
	measures                       map[string]int64 // nil when not measured
}

// Complete says the copy taken before the release holds all the data:
// true when none was needed.
func (w *ReleaseWitness) Complete() bool {
	return w.missing == "" && (w.Copy == nil || w.Copy.Status == BackupOK)
}

// CopyWords say what the copy taken before the release is, for a person:
// its id and status, or why none was taken.
func (w *ReleaseWitness) CopyWords() string {
	switch {
	case w.missing != "":
		return "no copy could be taken before this release: " + w.missing
	case w.Copy == nil:
		return "no copy was taken before this release: " + w.Passed
	case w.Copy.Status == BackupOK:
		return fmt.Sprintf("the copy taken before this release is backup %s (complete)", w.Copy.ID)
	}
	var missing []string
	for _, it := range w.Copy.Items {
		if it.Error != "" {
			missing = append(missing, it.Error)
		}
	}
	if len(missing) == 0 && w.Copy.Error != "" {
		missing = append(missing, w.Copy.Error)
	}
	return fmt.Sprintf("the copy taken before this release is backup %s, %s: %s", w.Copy.ID, w.Copy.Status, strings.Join(missing, "; "))
}

// BeforeRelease takes the copy a landing takes before it releases onto
// deploymentName, and reads each need's storage and measures there.
// synthetic says the deployment's data is synthetic (a reset may let it
// go). A deployment never released, holding no data, or on a target that
// keeps its own backups needs no copy; an error is a copy that could not be
// taken, which is not "nothing to copy".
func (s *Service) BeforeRelease(ctx context.Context, projectID ids.ID, deploymentName string, synthetic bool) (*ReleaseWitness, error) {
	w := &ReleaseWitness{synthetic: synthetic}
	dep, err := s.deployments.ByName(ctx, projectID, deploymentName)
	if errors.Is(err, sql.ErrNoRows) {
		w.Passed = "it has never been released"
		return w, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the deployment %s: %w", deploymentName, err)
	}
	if !dep.Standing() || dep.ReleaseSHA == "" {
		w.Passed = "nothing is released there yet"
		return w, nil
	}
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading its deployment target: %w", deploymentName, err)
	}
	if err := t.HoldsBackups(); err != nil {
		w.Passed = err.Error()
		w.Notes = append(w.Notes, "its data is on "+t.Name+", which AppTown does not witness")
		return w, nil
	}
	b, err := s.TakeBackup(ctx, projectID, deploymentName, TriggerPreRelease, "apptown-system")
	switch {
	case errors.Is(err, ErrNothingToBackUp):
		w.Passed = "it holds no data"
	case errors.Is(err, ErrHeldDataAbsent):
		// Named and not there is not "nothing to copy": the release could
		// boot on new, empty storage in its place.
		w.missing = "the data its release names is not on its host"
	case err != nil:
		return nil, fmt.Errorf("taking the copy before this release: %w", err)
	default:
		w.Copy = b
	}
	if t.DataAccess() != deployment.DataOnDockerHost {
		w.Notes = append(w.Notes, "its needs' data is on "+t.Name+", which AppTown does not witness")
		return w, nil
	}
	if err := s.readNeeds(ctx, w, dep, t); err != nil {
		w.Notes = append(w.Notes, "its needs' data was not witnessed: "+err.Error())
	}
	return w, nil
}

// readNeeds reads, for each need the running release declares that keeps
// data, its service's storage and measures.
func (s *Service) readNeeds(ctx context.Context, w *ReleaseWitness, dep *deployment.Deployment, t *deployment.Target) error {
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return err
	}
	w.dk = dk
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("reading the project: %v", err)
	}
	c, err := s.releaseContract(ctx, dep)
	if err != nil {
		return err
	}
	names, err := dep.HostNames(p.Slug)
	if err != nil {
		return err
	}
	for _, need := range c.Needs {
		kind, err := s.catalog.ByCapability(ctx, need)
		if err != nil || kind == nil || kind.Local.Data == "" {
			continue
		}
		f := needFacts{need: need, container: names.Service(need), path: kind.Local.Data, measure: kind.Local.Measure}
		there, err := dk.Exists(ctx, "inspect", f.container)
		if err != nil {
			return fmt.Errorf("the %s service: %v", need, err)
		}
		if !there {
			continue
		}
		if f.mount, f.created, err = storageOf(ctx, dk, f.container, f.path); err != nil {
			return fmt.Errorf("the %s service: %v", need, err)
		}
		if f.measure == "" {
			w.Notes = append(w.Notes, "the "+need+" need declares no measure, so how much it holds is not witnessed")
		} else if f.measures, err = measureNeed(ctx, dk, f.container, f.measure); err != nil {
			w.Notes = append(w.Notes, fmt.Sprintf("how much the %s need held before was not read (%v), so it is not compared", need, err))
		}
		w.needs = append(w.needs, f)
	}
	return nil
}

// storageOf is where a need service keeps its data, and when that volume
// was made ("" for a bind mount).
func storageOf(ctx context.Context, dk docker.Scope, container, path string) (docker.DataMount, string, error) {
	m, err := docker.DataMountAt(ctx, dk, container, path)
	if err != nil || m.Volume == "" {
		return m, "", err
	}
	out, err := dk.Run(ctx, "volume", "inspect", "-f", "{{.CreatedAt}}", m.Volume)
	if err != nil {
		return m, "", fmt.Errorf("reading volume %s: %v: %s", m.Volume, err, strings.TrimSpace(string(out)))
	}
	return m, strings.TrimSpace(string(out)), nil
}

// measureNeed runs a need's declared measure in its service and reads its
// "name value" lines.
func measureNeed(ctx context.Context, dk docker.Scope, container, measure string) (map[string]int64, error) {
	out, err := dk.Run(ctx, "exec", container, "sh", "-c", measure)
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	got := map[string]int64{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		got[f[0]] = n
	}
	if len(got) == 0 {
		return nil, fmt.Errorf("it printed no measures: %q", strings.TrimSpace(string(out)))
	}
	return got, nil
}

// Continuity floors: a measure is compared only when it was at least its
// floor before, so a small app's ordinary churn is not a failure. One that
// fell by more than half from there is (and tables going from some to none
// always is). Stated in the deployment guide.
var continuityFloors = map[string]int64{"rows": 20, "objects": 5, "keys": 5, "tables": 1, "bytes": 1 << 20}

// After reads each need again once the release has booted and says what
// is wrong with its data: problems fail the release step; notes are what
// was not compared. declared is the needs the new release declares: one it
// no longer declares is not compared.
func (w *ReleaseWitness) After(ctx context.Context, declared []string) (problems, notes []string) {
	notes = append(notes, w.Notes...)
	for _, f := range w.needs {
		if !slices.Contains(declared, f.need) {
			notes = append(notes, "the "+f.need+" need is no longer declared, so its data is not compared")
			continue
		}
		mount, created, err := storageOf(ctx, w.dk, f.container, f.path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("where the %s need keeps its data could not be read after the release: %v", f.need, err))
			continue
		}
		moved := mount.Spec() != f.mount.Spec() || created != f.created
		if moved && w.synthetic && mount.Volume != "" {
			// A deliberate reset: synthetic data, now on storage made
			// during this release. Nothing else makes a need new storage.
			notes = append(notes, "the "+f.need+" need's synthetic data was reset, as its new parameters required")
			continue
		}
		if moved {
			problems = append(problems, fmt.Sprintf("the %s need's data was on %s and is now on %s", f.need, describeStorage(f.mount, f.created), describeStorage(mount, created)))
			continue
		}
		if f.measures == nil {
			continue
		}
		after, err := measureNeed(ctx, w.dk, f.container, f.measure)
		if err != nil {
			problems = append(problems, fmt.Sprintf("how much the %s need holds could not be read after the release (%v); it held %s before", f.need, err, measureWords(f.measures)))
			continue
		}
		problems = append(problems, fallen(f.need, f.measures, after)...)
	}
	return problems, notes
}

// fallen is each measure of need that fell by more than half from at
// least its floor, or tables that went from some to none.
func fallen(need string, before, after map[string]int64) []string {
	var out []string
	names := make([]string, 0, len(before))
	for k := range before {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		b := before[k]
		a, ok := after[k]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("the %s need's %s could not be read after the release; it was %d before", need, k, b))
		case k == "tables" && b > 0 && a == 0:
			out = append(out, fmt.Sprintf("the %s need had %d tables and now has none", need, b))
		case b >= floorOf(k) && a*2 < b:
			out = append(out, fmt.Sprintf("the %s need's %s fell from %d to %d", need, k, b, a))
		}
	}
	return out
}

func floorOf(k string) int64 {
	if f, ok := continuityFloors[k]; ok {
		return f
	}
	return 1
}

func describeStorage(m docker.DataMount, created string) string {
	switch {
	case m.Volume != "" && created != "":
		return fmt.Sprintf("volume %s (made %s)", m.Volume, created)
	case m.Volume != "":
		return "volume " + m.Volume
	case m.Bind != "":
		return "the host folder " + m.Bind
	}
	return "no storage of its own"
}

func measureWords(m map[string]int64) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
