package verification

import (
	"context"
	"io/fs"
	"log"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/strs"
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
// bindings (provider "tool"). Config edits bump versions like any binding.
func SyncStockScanners(ctx context.Context, integrations *integration.Service, fsys fs.FS) {
	for _, m := range loadScannerManifests(fsys) {
		if err := integrations.MarkScannerManaged(ctx, m.Name); err != nil {
			log.Printf("marking scanner %s managed: %v", m.Name, err)
		}
		config, err := strs.JSON(map[string]any{
			"description": m.Description, "image": m.Image, "command": m.Command,
			"report": m.Report, "network": m.Network, "format": m.Format, "kind": m.Kind,
			"artifactKind": m.ArtifactKind, "target": m.Target, "manual": m.Manual,
		})
		if err == nil {
			_, err = integrations.Bindings.Upsert(ctx, m.Name, ScannerCapability, "tool", config, true)
		}
		if err != nil {
			log.Printf("integration %s: recording its configuration: %v", m.Name, err)
		}
	}
}
