package check

import (
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/execution"
)

// A Scanner is a maintained tool a way of working can carry: a container, a
// command, and what to do with what it leaves behind. Reading the catalogue
// these come from, and binding one to an instance, is the caller's.

type Scanner struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Image       string   `yaml:"image" json:"image"`
	Command     []string `yaml:"command" json:"command"`
	Report      string   `yaml:"report" json:"report"`
	Network     bool     `yaml:"network" json:"network"`
	// Format enables the structured-findings layer (findings.md): osv | sarif.
	// Empty = exit-code contract only. Kind labels SARIF findings (secret|sast|license).
	Format string `yaml:"format" json:"format"`
	Kind   string `yaml:"kind" json:"kind"`
	// ArtifactKind overrides the report artifact's kind (default scan-report);
	// e.g. "sbom" for syft — evidence of what shipped, not findings.
	ArtifactKind string `yaml:"artifactKind" json:"artifactKind"`
	// Target: what the scanner looks at — "source" (the committed tree,
	// default), "image" (the app's built container image, handed over
	// as a saved tarball at /work/.apptown/image.tar; use ${IMAGE_TAR} in
	// the command), or "exec-image" (run the command INSIDE the app's built
	// image — a framework self-check that must import settings;
	// design/subsystems/conformance.md §13.5). Scan POINTS in system workflows
	// (`with: {on: image}`) expand to every attached scanner with the matching
	// target (design/decisions/workflows-as-lanes.md).
	Target string `yaml:"target" json:"target"`
	// HistoryCommand: a source scanner that can read Git history checks,
	// at a landing, every commit the landing brings to the default branch,
	// not only its final tree: this command runs instead of Command, with
	// the project's repository mounted read-only at /history and
	// ${HISTORY_RANGE} naming those commits (target..candidate). A secret
	// added in one commit and removed in the next still lands in history,
	// which only a history scan sees.
	HistoryCommand []string `yaml:"historyCommand" json:"historyCommand"`
	// Manual: this scanner is invoked ONLY by name (a way-of-working `tool`
	// check, design/subsystems/conformance.md §13), never auto-expanded at a scan point.
	// It keeps a way-of-working-specific tool (a Django linter) from firing
	// fleet-wide the way a universal scanner (gitleaks) does.
	Manual bool `yaml:"manual" json:"manual"`
	// NoEntrypoint clears the image's entrypoint, so Command (and
	// HistoryCommand) is the whole command: a networked scanner whose image starts its program directly
	// runs the extra certificates' script first (internal/trust) with
	// ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; exec \"$@\"", "sh", <program>, ...].
	NoEntrypoint bool `yaml:"noEntrypoint" json:"noEntrypoint"`
	// AppliesTo is what a scan-point scanner applies to (applies.go),
	// required for one that is not manual. On a manual scanner it optionally
	// limits the applicability of the check that names it.
	AppliesTo []string `yaml:"appliesTo" json:"appliesTo,omitempty"`
	// EmptyExit is the exit the tool gives when it found nothing to judge
	// (osv-scanner's 128, "no package sources found"). Where the scanner
	// applies, that is a failure: the contract says there is something to
	// judge. 0: the tool has no such exit.
	EmptyExit int `yaml:"emptyExit" json:"emptyExit,omitempty"`
}

// ValidateApplicability holds a manifest to its applicability rule: a
// scanner that is not manual states what it applies to; a manual one may
// further limit what the check that names it applies to.
func (s Scanner) ValidateApplicability() error {
	if s.Manual && len(s.AppliesTo) == 0 {
		return nil
	}
	if err := ValidateAppliesTo(s.AppliesTo); err != nil {
		return fmt.Errorf("scanner %s: %w", s.Name, err)
	}
	return nil
}

// EmptyExitDetail is what a scanner's nothing-to-judge exit says where the
// scanner applies.
func EmptyExitDetail(scanner string, pl Place) string {
	where := "the repository"
	if pl.Parts != nil {
		where = strings.Join(pl.Parts, ", ") + " (" + pl.Folder + ")"
	}
	return fmt.Sprintf("%s found nothing to judge in %s, though the contract says there is: commit what %s reads (a lockfile), or correct the part's tools in .apptown/app.yaml", scanner, where, scanner)
}

// Looks says what the scanner looks at, with "source" as the default.
func (s Scanner) Looks() string {
	switch s.Target {
	case "image", "exec-image":
		return s.Target
	default:
		return "source"
	}
}

// Request is the scanner's run over workspace with argv (its Command, or
// HistoryCommand, its variables filled in): its image, with its entrypoint
// cleared when it says so, the network only when it needs it, and the scan
// timeout. Every place that runs a scanner starts from it, so what the
// manifest says reaches the container the same way everywhere.
func (s Scanner) Request(workspace string, argv []string) execution.Request {
	network := "none"
	if s.Network {
		network = "" // Docker's default: egress
	}
	return execution.Request{Workspace: workspace, Argv: argv, Image: s.Image, Network: network, NoEntrypoint: s.NoEntrypoint, Timeout: ScanTimeout}
}
