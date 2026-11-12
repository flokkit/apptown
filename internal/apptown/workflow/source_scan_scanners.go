package workflow

import "slices"

// SourceScanScanners captures the bindings expected by this run's source scan
// points at execution start. The retained receipts keep this set after purge.
func (e *Execution) SourceScanScanners() ([]string, error) {
	steps, err := readSteps(e.Context(), e.service.db, e.run.ID)
	if err != nil {
		return nil, err
	}
	scanners := []string{}
	for _, step := range steps {
		if step.Type != "scan" {
			continue
		}
		var cfg struct {
			On      string `json:"on"`
			Scanner string `json:"scanner"`
		}
		if err := DecodeStepConfig(step.Config, &cfg); err != nil {
			return nil, err
		}
		if cfg.On != "" && cfg.On != "source" {
			continue
		}
		if !slices.Contains(scanners, cfg.Scanner) {
			scanners = append(scanners, cfg.Scanner)
		}
	}
	slices.Sort(scanners)
	return scanners, nil
}
