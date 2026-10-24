package main

import (
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/toolversions"
)

// loadStock reads what this release shipped with, from dir (-shipped, else
// share/apptown/updates beside the binary): the ways-of-working update,
// whose content is AppTown's stock, and the tool versions update, which
// fills the tool versions owner the App is built with. A missing or unreadable file, or one this build
// cannot read, is an error: AppTown does not start without its stock, and
// has no compiled-in copy to fall back on.
func loadStock(dir, version string) (apptown.Stock, error) {
	dir, err := toolversions.ShippedDir(dir)
	if err != nil {
		return apptown.Stock{}, err
	}
	tools, err := toolversions.Shipped(dir, version)
	if err != nil {
		return apptown.Stock{}, fmt.Errorf("the tool versions this release shipped with: %w", err)
	}
	u, err := stockupdate.Shipped(dir, stockupdate.WaysOfWorking)
	if err != nil {
		return apptown.Stock{}, fmt.Errorf("the ways of working this release shipped with: %w", err)
	}
	if err := schema.CheckAPIVersion(u.Header.APIVersion); err != nil {
		return apptown.Stock{}, fmt.Errorf("%s: %w", u.Path, err)
	}
	judged, err := u.Header.Judge(version)
	if err != nil {
		return apptown.Stock{}, fmt.Errorf("%s: %w", u.Path, err)
	}
	if judged == stockupdate.NotJudged {
		log.Printf("stock: %s (works-with not judged: a maintainer's build has no release version)", u.Path)
	} else {
		log.Printf("stock: %s", u.Path)
	}
	return apptown.Stock{FS: u.Content, Name: u.Header.Name, Tools: toolversions.New(tools)}, nil
}
