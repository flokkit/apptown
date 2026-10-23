package main

import (
	"fmt"
	"path/filepath"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/trust"
)

// trustDir is where the trust files git reads are written, under the data
// directory.
const trustDir = "trust"

// installTrust loads each use's bundle from its flag and installs the
// process's trust before anything makes a connection.
func installTrust(dataDir string, paths map[trust.Use]string, local *console.LocalTLS) error {
	set, err := loadTrust(dataDir, paths, local)
	if err != nil {
		return err
	}
	trust.Install(set)
	return nil
}

// loadTrust reads the bundles. Under -local-tls AppTown's own authority
// joins the deployments use only: previews and deployments are its
// addresses; model providers and AppTown's other connections never answer
// with it.
func loadTrust(dataDir string, paths map[trust.Use]string, local *console.LocalTLS) (*trust.Set, error) {
	bundles, err := trust.Read(paths)
	if err != nil {
		return nil, err
	}
	if local != nil {
		ca, err := trust.Parse(local.CACert)
		if err != nil {
			return nil, fmt.Errorf("-local-tls: %w", err)
		}
		bundles[trust.Deployments] = bundles[trust.Deployments].With(ca)
	}
	return trust.New(filepath.Join(dataDir, trustDir), bundles)
}
