package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// imageFailOn is the stock landing's image scan threshold (its scan-image
// step writes failOn: high): a stock base image lists dozens of OS CVEs,
// most with no fix.
const imageFailOn = "high"

// firstReleaseNote is the base note of a project that has never released:
// there is nothing earlier to compare with, and that is not a fault.
const firstReleaseNote = "first release: there is no earlier image to compare with, so every finding counts as new"

// RunImageScans is apptown check's view of the landing's image scan point:
// every attached, non-manual scanner whose target is the built image runs
// against the image of the checked commit, compared with the same base the
// landing's scan-image step uses, and judged as that step judges
// (dispositions applied, carried findings apart, failing at imageFailOn).
// The answer is feedback: nothing is recorded, and the landing scans the
// image it lands again. findings are the failing ones.
func (s *Scanners) RunImageScans(ctx context.Context, projectID ids.ID, src, sha, repo, baseSHA, namePrefix string) (results Results, findings []conformance.Finding, scanErrors int) {
	attached, err := s.attachedScanners(ctx)
	if err != nil {
		return Results{{Check: "image scanners", Evaluator: "scanner", Verdict: workflow.VerdictError, AppTownFault: true,
			Output: "reading the attached scanners: " + err.Error()}}, nil, 1
	}
	var dispositions []conformance.Disposition
	read := false
	for _, sc := range attached {
		m := sc.M
		if m.Manual || m.Looks() != "image" {
			continue
		}
		res := workflow.CheckReceipt{Check: sc.Name, Scanner: sc.Name, Evaluator: "scanner"}
		fail := func(why string) {
			res.Verdict, res.Output = workflow.VerdictError, strs.Truncate(why, 300)
			scanErrors++
			results = append(results, res)
		}
		start := s.now()
		ref, note, status, err := s.imageForCommit(ctx, projectID, src, sha, true)
		switch {
		case err != nil:
			fail("the image to scan: " + err.Error())
			continue
		case ref == "":
			if status != "none" {
				fail("no image to scan: " + note)
				continue
			}
			res.Verdict, res.Output = workflow.VerdictPass, strings.Replace(note, "nothing to run in an image", "nothing to scan as an image", 1)
			results = append(results, res)
			continue
		}
		if !read {
			if dispositions, err = s.conformance.Dispositions(ctx, projectID, true); err != nil {
				fail("reading exceptions: " + err.Error())
				continue
			}
			read = true
		}
		argv := append([]string{}, m.Command...)
		base := s.baseImageFingerprints(ctx, projectID, repo, baseSHA, m, sc.Name, src, argv, namePrefix+"-scan-"+sc.Name)
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.apptown/image.tar")
		}
		if err := clearForScan(src, m); err != nil {
			fail(err.Error())
			continue
		}
		req := m.Request(src, argv)
		req.Name, req.Report, req.ExportImage = execution.UniqueName(namePrefix+"-scan-"+sc.Name), m.Report, ref
		answer := readScan(s.exec.Run(ctx, req), m, sc.Name)
		res.DurationMs = new(s.now().Sub(start).Milliseconds())
		if answer.failure != "" {
			fail(answer.failure)
			continue
		}
		if m.Format == "" || m.Report == "" {
			// No structured report: the exit-code contract, as the gate
			// reads it. Exit 1 is findings, and no base can say they are
			// carried.
			if answer.exit == 1 {
				res.Verdict = workflow.VerdictFail
				res.Output = sc.Name + " reported findings (exit 1)"
				findings = append(findings, conformance.Finding{Finding: check.Finding{
					Fingerprint: "scan|" + sc.Name, Kind: "scan", Severity: "high",
					Title: sc.Name + " reported findings", Source: sc.Name,
				}})
			} else {
				res.Verdict = workflow.VerdictPass
			}
			results = append(results, res)
			continue
		}
		_, gating, carried := scanGate(answer.findings, dispositions, base, true)
		if base.Note != "" {
			res.BaseNote = base.Note
		}
		if len(carried) > 0 {
			res.Carried = len(carried)
			res.CarriedNote = fmt.Sprintf("%d already on main (owed debt, not gating): %s", len(carried), carriedSummary(carried))
		}
		var failing []conformance.Finding
		for _, f := range gating {
			if check.SeverityRank(f.Severity) >= check.SeverityRank(imageFailOn) {
				failing = append(failing, f)
			}
		}
		if len(failing) > 0 {
			res.Verdict, res.Output = workflow.VerdictFail, scanFindingsSummary(sc.Name, failing)
			findings = append(findings, failing...)
		} else {
			res.Verdict, res.Output = workflow.VerdictPass, res.CarriedNote
		}
		results = append(results, res)
	}
	return results, findings, scanErrors
}

// baseImageFingerprints scans the image a change is compared with: the
// image of its base commit (baseSHA), and nothing else — the release main
// serves can be another commit, so it never stands in. A base image it
// cannot prepare is unavailable, never clean, and its note says why.
func (s *Scanners) baseImageFingerprints(ctx context.Context, projectID ids.ID, repo, baseSHA string, m check.Scanner, binding, scanSrc string, argvTemplate []string, name string) scanBase {
	b := scanBase{}
	imgID, how := "", ""
	preparation := "no starting revision recorded"
	if baseSHA != "" {
		dir, cleanup, err := scm.StageTreeAt(ctx, repo, baseSHA)
		if err != nil {
			preparation = "staging starting revision " + strs.ShortSHA(baseSHA) + " failed: " + strs.Truncate(err.Error(), 300)
		} else {
			ref, note, status, err := s.imageForCommit(ctx, projectID, dir, baseSHA, true)
			cleanup()
			switch {
			case err != nil:
				preparation = "preparing starting revision's image failed: " + strs.Truncate(err.Error(), 400)
			case status == "ok" && ref != "":
				imgID, how = ref, "starting revision's image at "+strs.ShortSHA(baseSHA)
			case status == "none":
				preparation = "no image of the starting revision to compare with: " + note
			default:
				preparation = "starting revision's image unavailable: " + note
			}
		}
	}
	if imgID == "" {
		// A project that has never released has nothing earlier to compare
		// with, wherever the checks run: say that, not why the starting
		// revision (often a seed that cannot build yet) gave no image. The
		// reason is kept as the detail. A deployment that cannot be read is
		// unknown, not "never released".
		e, err := s.deployments.ByName(ctx, projectID, "main")
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && e.ReleaseSHA == ""):
			b.Note, b.Detail = firstReleaseNote, preparation
		case err != nil:
			b.Note = preparation + "; reading the serving release failed: " + strs.Truncate(err.Error(), 300) + " — every finding is treated as introduced"
		default:
			b.Note = preparation + " — every finding is treated as introduced"
		}
		return b
	}
	b.SHA = imgID
	// The SAME tar path as the real scan: the scanner folds the artifact path
	// into every OS-package finding's fingerprint and location, so a
	// different name would make identical CVEs look introduced. Each scan
	// clears the path before it runs (clearForScan), so the real scan never
	// reads the base's image or report.
	argv := append([]string{}, argvTemplate...)
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.apptown/image.tar")
	}
	if err := clearForScan(scanSrc, m); err != nil {
		b.Note = "scanning the base commit's image failed (" + strs.Truncate(err.Error(), 160) + ") — every finding is treated as introduced"
		return b
	}
	req := m.Request(scanSrc, argv)
	req.Name, req.Report, req.ExportImage = execution.UniqueName(name+"-base"), m.Report, imgID
	answer := readScan(s.exec.Run(ctx, req), m, binding)
	if answer.failure != "" {
		b.Note = "scanning the base commit's image failed (" + strs.Truncate(answer.failure, 160) + ") — every finding is treated as introduced"
		return b
	}
	b.Findings = conformance.RawFindings(answer.findings)
	b.OK = true
	b.Note = "compared with " + how
	return b
}
