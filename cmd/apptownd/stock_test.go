package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/stockupdate/stockbuild"
	"github.com/flokkit/apptown/internal/stockupdate/stockupdatetest"
	"github.com/flokkit/apptown/stock"
)

// apptownd starts with the stock its release shipped: the ways-of-working
// update's content, named, and the tool versions update's list.
func TestAppTownStartsWithTheStockItsReleaseShipped(t *testing.T) {
	s, err := loadStock(stockupdatetest.Dir(t), "dev-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != stockupdatetest.Name || s.Tools == nil || !s.Tools.HasLine("node", "24") {
		t.Fatalf("stock %q, tools %v", s.Name, s.Tools)
	}
	if _, err := fs.Stat(s.FS, "packs.yaml"); err != nil {
		t.Fatalf("the stock read is not the shipped content: %v", err)
	}
}

// Without its stock, apptownd does not start: a missing folder, a missing
// update of either series, and an update this release cannot read are each
// an error naming what was missing or why, never an empty library.
func TestAppTownDoesNotStartWithoutItsStock(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := loadStock(missing, "dev-x"); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Errorf("a missing folder: %v", err)
	}
	for _, drop := range []stockupdate.Series{stockupdate.WaysOfWorking, stockupdate.ToolVersions} {
		dir := stockupdatetest.Dir(t)
		if err := os.Remove(filepath.Join(dir, stockbuild.FileName(drop, stockupdatetest.Name))); err != nil {
			t.Fatal(err)
		}
		if _, err := loadStock(dir, "dev-x"); !errors.Is(err, stockupdate.ErrNoUpdate) {
			t.Errorf("without its %s update: %v", drop, err)
		}
	}
	// A release reads only what works with it.
	dir := t.TempDir()
	ways, err := stock.WaysOfWorking(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	ways["update.yaml"] = []byte("apiVersion: apptown.dev/v1\nkind: WaysOfWorkingUpdate\nworksWith: \">=9.0.0\"\n")
	for series, payload := range map[stockupdate.Series]map[string][]byte{stockupdate.WaysOfWorking: ways, stockupdate.ToolVersions: {stock.ToolVersionsFile: stock.ToolVersions}} {
		b, err := stockbuild.Build(series, payload, "v9.0.0", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stockbuild.FileName(series, "v9.0.0")), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadStock(dir, "v1.0.0"); !errors.Is(err, stockupdate.ErrIncompatible) {
		t.Errorf("an update for a later release: %v", err)
	}
}
