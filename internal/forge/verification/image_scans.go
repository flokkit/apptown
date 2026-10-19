package verification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"
)

// imageFailOn is the image scan point's default threshold (RunScan): a
// stock base image lists dozens of OS CVEs, most with no fix.
const imageFailOn = "high"

// firstReleaseNote is the base note of a project that has never released:
// there is nothing earlier to compare with, and that is not a fault.
const firstReleaseNote = "first release: there is no earlier image to compare with, so every finding counts as new"

// RunImageScans is forge check's view of the landing's image scan point:
// every attached, non-manual scanner whose target is the built image runs
// against the image of the checked commit, compared with the same base the
// landing's scan-image step uses, and judged as that step judges
// (dispositions applied, carried findings apart, failing at imageFailOn).
// The answer is feedback: nothing is recorded, and the landing scans the
// image it lands again. findings are the failing ones.
func (s *Scanners) RunImageScans(ctx context.Context, projectID ids.ID, src, sha, repo, baseSHA, namePrefix string) (results Results, findings []conformance.Finding, scanErrors int) {
	attached, err := s.attachedScanners(ctx)
	if err != nil {
		return Results{{Check: "image scanners", Evaluator: "scanner", Verdict: workflow.VerdictError, ForgeFault: true,
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
			argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.forge/image.tar")
		}
		if m.Report != "" {
			_ = os.Remove(filepath.Join(src, m.Report))
		}
		network := "none"
		if m.Network {
			network = ""
		}
		answer := readScan(s.exec.Run(ctx, execution.Request{
			Workspace: src, Argv: argv, Image: m.Image, Network: network, Timeout: check.ScanTimeout,
			Name: execution.UniqueName(namePrefix + "-scan-" + sc.Name), Report: m.Report, ExportImage: ref,
		}), m, sc.Name)
		// The image is saved into the tree only if no file is there: the
		// next image scanner's base and head scans save theirs to the same
		// path.
		_ = os.Remove(filepath.Join(src, ".forge", "image.tar"))
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
// starting revision's image, else the image the main deployment serves.
// A base it cannot read is unavailable, never clean, and its note says why.
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
	var serving *deployment.Deployment
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
			return b
		case err != nil:
			b.Note = preparation + "; reading the serving release failed: " + strs.Truncate(err.Error(), 300) + " — every finding is treated as introduced"
			return b
		}
		serving = e
	}
	absent := s.docker != nil && s.docker.Absent()
	if imgID == "" && (!s.exec.Local || absent) {
		// The serving release runs on this machine; checks run on the
		// worker, which does not have its image. The baseline is
		// unavailable, never clean. With no local Docker it is unknown.
		if absent {
			// Said plainly: "no local Docker" reads as a fault to fix,
			// but it is how this forge runs.
			b.Note = "no earlier image to compare with, so every finding is treated as introduced by this change: the image of the commit it started from was not available (" + preparation + "), and this AppTown runs no Docker of its own, so it cannot read the image the app runs now"
			return b
		}
		b.Note = preparation + "; the serving release image is on AppTown's host, not the worker — every finding is treated as introduced"
		return b
	}
	if imgID == "" {
		container := serving.Container
		if container == "" {
			b.Note = preparation + "; no serving release image — every finding is treated as introduced"
			return b
		}
		if !s.docker.Running(ctx, container) {
			b.Note = preparation + "; the main deployment is not running — every finding is treated as introduced"
			return b
		}
		imgID = s.docker.Text(ctx, "inspect", "-f", "{{.Image}}", container)
		if imgID == "" {
			b.Note = preparation + "; could not read the serving image — every finding is treated as introduced"
			return b
		}
		how = "the serving release image " + strs.Truncate(strings.TrimPrefix(imgID, "sha256:"), 12) + " (" + preparation + ")"
	}
	b.SHA = imgID
	// The SAME tar path as the real scan: the scanner folds the artifact path
	// into every OS-package finding's fingerprint and location, so a
	// different name would make identical CVEs look introduced. The base
	// scan runs first and removes its tar before the real image is saved.
	defer os.Remove(filepath.Join(scanSrc, ".forge", "image.tar"))
	argv := append([]string{}, argvTemplate...)
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.forge/image.tar")
	}
	if m.Report != "" {
		_ = os.Remove(filepath.Join(scanSrc, m.Report))
	}
	network := "none"
	if m.Network {
		network = ""
	}
	answer := readScan(s.exec.Run(ctx, execution.Request{
		Workspace: scanSrc, Argv: argv, Image: m.Image, Network: network, Timeout: check.ScanTimeout,
		Name:        execution.UniqueName(name + "-base"),
		Report:      m.Report,
		ExportImage: imgID,
	}), m, binding)
	if m.Report != "" {
		_ = os.Remove(filepath.Join(scanSrc, m.Report)) // the real scan writes its own
	}
	if answer.failure != "" {
		b.Note = "scanning the serving image failed (" + strs.Truncate(answer.failure, 160) + ") — every finding is treated as introduced"
		return b
	}
	b.Findings = conformance.RawFindings(answer.findings)
	b.OK = true
	b.Note = "compared with " + how
	return b
}
