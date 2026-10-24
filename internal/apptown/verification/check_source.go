package verification

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// CheckSource is the one reading of a staged tree that every judging of
// declared checks starts from (the gate, apptown check, a fleet review, the
// baseline, what-if, an eval): the tree, and what its own .apptown/app.yaml
// declares, so a check's applicability is decided the same way everywhere.
// A base tree is read with the base's contract. Read it before anything runs
// in the tree, since a tool writes its report there.
func CheckSource(tree string) check.Source {
	src := check.Source{Tree: tree, Empty: treeEmpty(tree)}
	c, err := docker.ReadWorkspaceContract(tree)
	switch {
	case errors.Is(err, docker.ErrNoContract):
		src.NoContract = true
	case err != nil:
		src.PartsErr = err
	default:
		src.Parts, src.NotAppCode = ContractParts(c), c.NotAppCode
	}
	return src
}

// ContractParts is the contract's parts in the terms applicability reads.
func ContractParts(c *appcontract.Contract) []check.Part {
	var out []check.Part
	for i := range c.Parts {
		p := &c.Parts[i]
		var kinds []string
		if p.Build != "" || p.StaticBuild != nil {
			kinds = append(kinds, check.KindBuild)
		}
		if p.IsStatic() {
			kinds = append(kinds, check.KindStatic)
		} else {
			kinds = append(kinds, check.KindContainer)
		}
		out = append(out, check.Part{Name: p.Name, Folder: p.Folder(), Kinds: kinds, Tools: p.Tools, Public: p.Public})
	}
	return out
}

// treeEmpty says the tree holds nothing of the project's: nothing but
// Git's own folder and the .apptown folder AppTown's tools write their
// reports in (a contract there is the project's). A tree that cannot be
// listed is not called empty: the runs that follow report it.
func treeEmpty(tree string) bool {
	entries, err := os.ReadDir(tree)
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch e.Name() {
		case ".git":
		case ".apptown":
			if _, err := os.Stat(filepath.Join(tree, ".apptown", "app.yaml")); !errors.Is(err, fs.ErrNotExist) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// decided is a check settled before it was judged, with its receipt.
type decided struct {
	Check   conformance.DeclaredCheck
	Receipt workflow.CheckReceipt
}

// splitApplicable takes the checks of one evaluator that are judged once
// for the whole app (a rubric, a runtime probe) out of checks when they
// apply to nothing src declares, and gives each its receipt: not
// applicable with the reason, or the error a contract that cannot be read
// makes. The rest, every other evaluator's included, are returned to judge.
func splitApplicable(checks []conformance.DeclaredCheck, evaluator string, src check.Source, mode check.Mode) (judge []conformance.DeclaredCheck, settled []decided) {
	for _, sc := range checks {
		if check.Evaluator(sc.Check) != evaluator {
			judge = append(judge, sc)
			continue
		}
		ev := check.NotApplicableHere(sc.Declared(), src, mode)
		if ev == nil {
			judge = append(judge, sc)
			continue
		}
		res := resultOf(*ev)
		res.Detail = ev.Detail
		settled = append(settled, decided{Check: sc, Receipt: res})
	}
	return judge, settled
}

// receipts are the settled checks' receipts, and how many are errors.
func receipts(ds []decided) (Results, int) {
	var out Results
	errs := 0
	for _, d := range ds {
		out = append(out, d.Receipt)
		if d.Receipt.Verdict == workflow.VerdictError {
			errs++
		}
	}
	return out, errs
}

// scannerApplies is a scanner's applicability: an image or exec-image
// scanner looks at the public part's image; any other judges the
// repository once when any declared part matches it.
func scannerApplies(m check.Scanner, src check.Source) (string, error) {
	if m.Looks() != "source" {
		return check.AppliesToPublic(m.AppliesTo, src)
	}
	return check.AppliesHere(m.AppliesTo, src)
}
