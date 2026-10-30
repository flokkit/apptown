package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/strs"
	"net/http"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

type Findings struct {
	projects    *project.Projects
	work        *workflow.Service
	conformance *conformance.Service
	db          *database.DB
	dataDir     string
}

func NewFindings(projects *project.Projects, work *workflow.Service, conformance *conformance.Service, db *database.DB, dataDir string) Findings {
	return Findings{projects: projects, work: work, conformance: conformance, db: db, dataDir: dataDir}
}

// RunFindings: GET /runs/{id}/findings — the normalized view.
func (h Findings) RunFindings(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	run, err := h.work.Run(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	reports, err := h.work.RunArtifacts(r.Context(), id, workflow.ArtifactSelection{Kinds: []string{"scan-report", "conformance-report"}})
	if err != nil {
		WriteFault(w, err)
		return
	}
	findings, unread, err := h.runFindings(r.Context(), reports)
	if err != nil {
		WriteFault(w, err)
		return
	}
	dispositions, err := h.conformance.Dispositions(r.Context(), run.ProjectID, true)
	if err != nil {
		WriteFault(w, err)
		return
	}
	conformance.AnnotateFindings(findings, dispositions)
	if findings == nil {
		findings = []conformance.Finding{}
	}
	steps, err := h.work.RunSteps(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	markCarried(steps, findings)
	total, dispositioned := len(findings), 0
	bySev := map[string]int{}
	for _, f := range findings {
		if f.Disposition != nil {
			dispositioned++
		} else {
			bySev[f.Severity]++
		}
	}
	slugMeta, slugErr := project.ByIDIn(r.Context(), h.db, run.ProjectID)
	slug := slugMeta.Slug
	if _, err := database.Lookup(slugErr); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, RunFindingsView{Findings: findings, Unread: unread, ProjectSlug: slug,
		Summary: FindingsSummary{Total: total, Dispositioned: dispositioned, Actionable: total - dispositioned, BySeverity: bySev}})
}

// ProjectFindings: GET /projects/{slug}/findings — the posture view
// (findings.md §5): latest scan report per scanner binding across the
// project's runs, normalized and disposition-annotated, plus the full
// disposition list for management.
func (h Findings) ProjectFindings(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// SEMANTICS: posture describes the project's ACCEPTED state, so only
	// COMPLETED (merged) runs contribute — an in-flight branch's findings
	// belong to that run's page, where they gate. Latest report per scanner
	// binding across done runs; staleness vs the default branch is surfaced,
	// not hidden. (The fully-correct end state is scanning the default
	// branch itself on merge — findings.md §5.)
	reps, err := h.work.AcceptedScanReports(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}

	findings := []conformance.Finding{}
	sources := []FindingSource{}
	for _, x := range reps {
		// The change a report belongs to is its root run's: scans often run
		// in a child run, which has no change of its own.
		source, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: x.RunID, ProjectID: p.ID})
		if err != nil {
			WriteFault(w, err)
			return
		}
		fs, unreadable, err := h.scanReportFindings(r.Context(), x.Name, x.Body)
		if err != nil {
			WriteFault(w, err)
			return
		}
		findings = append(findings, fs...)
		sources = append(sources, FindingSource{Binding: x.Name, RunID: x.RunID, RunNumber: source.RunNumber, ChangeID: source.ChangeID, ChangeNumber: source.ChangeNumber, At: x.CreatedAt, Findings: len(fs), Unreadable: unreadable})
	}
	active, err := h.conformance.Dispositions(r.Context(), p.ID, true)
	if err != nil {
		WriteFault(w, err)
		return
	}
	conformance.AnnotateFindings(findings, active)
	total, dispositioned := len(findings), 0
	bySev := map[string]int{}
	for _, f := range findings {
		if f.Disposition != nil {
			dispositioned++
		} else {
			bySev[f.Severity]++
		}
	}
	// Full disposition list (incl. expired) for management.
	dispositions, err := h.conformance.Dispositions(r.Context(), p.ID, false)
	if err != nil {
		WriteFault(w, err)
		return
	}
	slices.Reverse(dispositions)

	// Staleness: has the default branch moved since the newest contributing
	// scan? (Approximate — compares commit time to scan time.)
	repoState := FindingsRepoState{}
	if p.DefaultBranch != "" {
		dir, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if sha, err := gitexec.Run(r.Context(), dir, "rev-parse", "--short", "HEAD"); err == nil {
			when, _ := gitexec.Run(r.Context(), dir, "log", "-1", "--format=%cI")
			subject, _ := gitexec.Run(r.Context(), dir, "log", "-1", "--format=%s")
			committedAt := strings.TrimSpace(when)
			repoState.SHA = strings.TrimSpace(sha)
			repoState.CommittedAt = &committedAt
			latestScan := ""
			var latestRun ids.ID
			for _, x := range reps {
				if x.CreatedAt > latestScan {
					latestScan, latestRun = x.CreatedAt, x.RunID
				}
			}
			// A run's own merge commit always postdates its scan but contains
			// exactly the scanned content — not stale. Stale = main's HEAD is
			// newer AND belongs to something other than the contributing run.
			rootID := latestRun
			if !latestRun.IsZero() {
				run, err := h.work.Run(r.Context(), latestRun)
				if err != nil {
					WriteError(w, 500, "reading contributing run: %v", err)
					return
				}
				source, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
				if err != nil {
					WriteFault(w, err)
					return
				}
				rootID = source.RootRunID // scans often live on a child; the merge names the root
			}
			ownMerge := strings.Contains(subject, fmt.Sprintf("run %d", rootID))
			stale := latestScan != "" && strings.TrimSpace(when) > latestScan && !ownMerge
			repoState.Stale = &stale
		}
	}
	unread := false
	for _, source := range sources {
		unread = unread || source.Unreadable != ""
	}
	WriteJSON(w, 200, ProjectFindingsView{StatusLabel: conformance.FindingsLabel(total-dispositioned, len(sources), unread), Findings: findings, Sources: sources, Dispositions: dispositions,
		ProjectSlug: p.Slug, Repo: repoState,
		Summary: FindingsSummary{Total: total, Dispositioned: dispositioned, Actionable: total - dispositioned, BySeverity: bySev}})
}

// scanReportFindings reads one accepted scan report through the scanner
// binding it names, which declares the report's format. A report that
// cannot be read that way (no scanner name, a scanner no longer configured,
// a configuration that does not parse) is not an empty report: it comes
// back with the reason, so the posture shows it as unread instead of
// leaving it out and reading clean. A failed database read is an error.
func (h Findings) scanReportFindings(ctx context.Context, name, body string) ([]conformance.Finding, string, error) {
	if name == "" {
		return nil, "the report names no scanner", nil
	}
	b, err := integration.Selected(ctx, h.db, "scanner", name)
	if found, err := database.Lookup(err); err != nil {
		return nil, "", err
	} else if !found {
		return nil, fmt.Sprintf("no scanner named %q is configured, so its report format is unknown", name), nil
	}
	var cfg struct {
		Format string `json:"format"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(b.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Sprintf("the %q scanner's configuration could not be read: %v", name, err), nil
	}
	fs, err := conformance.ReadReport(cfg.Format, []byte(body), name, cfg.Kind)
	if err != nil {
		return nil, fmt.Sprintf("the report could not be read as %s: %v", strs.FirstNonBlank(cfg.Format, "its scanner's format"), err), nil
	}
	return fs, "", nil
}

// runFindings computes the normalized findings for a
// run from its scan-report artifacts (layer 2: a pure view over evidence).
// A report that cannot be read comes back in unread with its reason, the
// same as on the project posture: its findings are unknown, not zero.
func (h Findings) runFindings(ctx context.Context, reps []workflow.Artifact) ([]conformance.Finding, []UnreadReport, error) {
	var findings []conformance.Finding
	unread := []UnreadReport{}
	for _, r := range reps {
		if r.Kind != "scan-report" {
			continue
		}
		// The artifact name is the scanner binding; its config declares format+kind.
		fs, reason, err := h.scanReportFindings(ctx, r.Name, r.Body)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			unread = append(unread, UnreadReport{Name: r.Name, Kind: r.Kind, Reason: reason})
			continue
		}
		findings = append(findings, fs...)
	}
	// Guidance-conformance findings are stored native — the step controls
	// its own stable fingerprints; no scanner binding is involved.
	for _, report := range reps {
		if report.Kind != "conformance-report" {
			continue
		}
		var native []conformance.Finding
		if err := json.Unmarshal([]byte(report.Body), &native); err != nil {
			unread = append(unread, UnreadReport{Name: report.Name, Kind: report.Kind, Reason: fmt.Sprintf("the %q conformance report could not be read: %v", report.Name, err)})
			continue
		}
		findings = append(findings, native...)
	}
	return findings, unread, nil
}

// markCarried flags the findings a run's scan steps judged as carried debt
// (already on the change's base — scan_provenance.go), so the findings view
// shows what the gate held the change to and what it merely carried.
func markCarried(steps []*workflow.Step, findings []conformance.Finding) {
	carried := map[string]bool{}
	for _, step := range steps {
		if step.Type != "scan" {
			continue
		}
		var m struct {
			Carried []string `json:"carriedFingerprints"`
		}
		if json.Unmarshal(step.Evidence, &m) == nil {
			for _, fp := range m.Carried {
				carried[fp] = true
			}
		}
	}
	for i := range findings {
		if carried[findings[i].Fingerprint] {
			findings[i].PreExisting = true
		}
	}
}

// RunFindingsView is one run's normalized, disposition-annotated findings.
// Unread lists the run's reports that could not be read: their findings
// are unknown, so an empty Findings with any Unread is not "nothing found".
type RunFindingsView struct {
	Findings    []conformance.Finding `json:"findings"`
	Unread      []UnreadReport        `json:"unread"`
	ProjectSlug string                `json:"projectSlug"`
	Summary     FindingsSummary       `json:"summary"`
}

// ProjectFindingsView is a project's accepted posture: the latest report per
// scanner, every disposition, and whether main has moved since the scan.
type ProjectFindingsView struct {
	StatusLabel  conformance.CheckLabel    `json:"statusLabel"`
	Findings     []conformance.Finding     `json:"findings"`
	Sources      []FindingSource           `json:"sources"`
	Dispositions []conformance.Disposition `json:"dispositions"`
	ProjectSlug  string                    `json:"projectSlug"`
	Repo         FindingsRepoState         `json:"repo"`
	Summary      FindingsSummary           `json:"summary"`
}

// FindingsSummary counts findings: all, those with a disposition, the rest,
// and the rest by severity.
type FindingsSummary struct {
	Total         int            `json:"total"`
	Dispositioned int            `json:"dispositioned"`
	Actionable    int            `json:"actionable"`
	BySeverity    map[string]int `json:"bySeverity"`
}

// UnreadReport is a run's report the findings view could not read, and why.
type UnreadReport struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// FindingSource is one scanner report that contributed to a posture.
// ChangeID is the change the report's run worked on (none for a run without
// one); RunNumber and ChangeNumber name them within the project (0: none). Unreadable, when set, says why the report could not be read: its
// findings are unknown, not zero.
type FindingSource struct {
	Binding      string `json:"binding"`
	RunID        ids.ID `json:"runId"`
	RunNumber    int64  `json:"runNumber"`
	ChangeID     ids.ID `json:"changeId"`
	ChangeNumber int64  `json:"changeNumber"`
	At           string `json:"at"`
	Findings     int    `json:"findings"`
	Unreadable   string `json:"unreadable,omitempty"`
}

// FindingsRepoState is main's head against the newest contributing scan;
// empty when the project has no repository.
type FindingsRepoState struct {
	SHA         string  `json:"sha,omitempty"`
	CommittedAt *string `json:"committedAt,omitempty"`
	Stale       *bool   `json:"stale,omitempty"`
}
