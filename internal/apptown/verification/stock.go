package verification

import (
	"context"
	"io/fs"
	"log"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/strs"
	yamlv3 "gopkg.in/yaml.v3"
)

// Stock scanner declarations are bound during instance bootstrap.
func loadScannerManifests(fsys fs.FS) []check.Scanner {
	raw, err := fs.ReadFile(fsys, "scanners.yaml")
	if err != nil {
		return nil
	}
	var doc struct {
		Scanners []check.Scanner `yaml:"scanners"`
	}
	if yamlv3.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return doc.Scanners
}

// SyncStockScanners upserts the stock scanner manifests as scanner-capability
// bindings (provider "tool"), each with its whole manifest as its config.
// Config edits bump versions like any binding.
func SyncStockScanners(ctx context.Context, integrations *integration.Service, fsys fs.FS) {
	for _, m := range loadScannerManifests(fsys) {
		if err := integrations.MarkScannerManaged(ctx, m.Name); err != nil {
			log.Printf("marking scanner %s managed: %v", m.Name, err)
		}
		// The binding holds the manifest as declared: its JSON keys are the
		// binding's config keys, so no declared field is left behind. A
		// hand-copied list once dropped historyCommand, and every landing
		// scanned only its final tree.
		config, err := strs.JSON(m)
		if err == nil {
			_, err = integrations.Bindings.Upsert(ctx, m.Name, ScannerCapability, "tool", config, true)
		}
		if err != nil {
			log.Printf("integration %s: recording its configuration: %v", m.Name, err)
		}
	}
}
