package workflow

import "slices"

// ScanScanners captures the bindings expected by this run's selected scan
// points at execution start. The retained receipts keep this set after purge.
func (e *Execution) ScanScanners(point string) ([]string, error) {
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
		if cfg.On == "" {
			cfg.On = "source"
		}
		if cfg.On != point {
			continue
		}
		if !slices.Contains(scanners, cfg.Scanner) {
			scanners = append(scanners, cfg.Scanner)
		}
	}
	slices.Sort(scanners)
	return scanners, nil
}
