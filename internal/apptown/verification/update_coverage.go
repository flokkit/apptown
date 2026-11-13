package verification

import (
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fix"
)

// updateCoverage uses the same part applicability as fix execution and listing.
// Every tool part needs a complete declaration, including targeted execution.
func updateCoverage(c *appcontract.Contract, ds []fix.Declaration) ([]check.Part, error) {
	var missing []check.Part
	for _, p := range contractParts(c) {
		if len(p.Tools) == 0 {
			continue
		}
		covered := false
		for _, d := range ds {
			kind, ok := fix.Lookup(d.Kind)
			if !ok || kind.Classifier != fix.UpdateClassifier {
				continue
			}
			places, _, err := check.Applies(d.AppliesTo, check.Source{Parts: []check.Part{p}})
			if err != nil {
				return nil, err
			}
			if len(places) > 0 && d.Run != "" && d.RunFor != "" && d.List != "" {
				covered = true
			}
		}
		if !covered {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// partsWithoutMajorCommand reports adopted update declarations without a major
// command. These parts still receive their ordinary update at either level.
func partsWithoutMajorCommand(c *appcontract.Contract, ds []fix.Declaration) ([]string, error) {
	var out []string
	for _, p := range contractParts(c) {
		for _, d := range ds {
			kind, ok := fix.Lookup(d.Kind)
			if !ok || kind.Classifier != fix.UpdateClassifier {
				continue
			}
			places, _, err := check.Applies(d.AppliesTo, check.Source{Parts: []check.Part{p}})
			if err != nil {
				return nil, err
			}
			if len(places) > 0 && d.RunMajor == "" {
				out = append(out, p.Name+" ("+d.Ecosystem+"): major versions not offered by the adopted pack")
			}
		}
	}
	return out, nil
}

// onlyChecksStarts reports whether no contract part declares a test command.
func onlyChecksStarts(c *appcontract.Contract) bool {
	for _, part := range c.Parts {
		if len(part.Test) > 0 {
			return false
		}
	}
	return true
}
