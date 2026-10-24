package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/stock"
)

func backupHTTPFixture(t *testing.T, slug string) (Backups, ids.ID, *dataprotection.Backups) {
	t.Helper()
	db := databaseSuite.New(t)
	projects := project.New(db, nil)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: strings.ToUpper(slug)})
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	targets := deployment.NewTargets(db, nil)
	hosts, environment := deployment.NewHosts(targets, nil, deployment.HostSettings{DataDir: dir}), deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{ToolVersions: stock.Versions(), Needs: catalog, Projects: projects})
	starter := dataprotection.NewStarter(db, dataprotection.StarterDependencies{Targets: targets, Hosts: hosts, Environment: environment, Catalog: catalog, DataDir: dir})
	svc := deployment.New(context.Background(), db, deployment.Dependencies{Targets: targets, Hosts: hosts, Environment: environment, CopyStarter: starter})
	data := dataprotection.New(starter, dataprotection.Dependencies{Deployments: svc.Deployments, Releases: svc.Releases, Bundles: svc.Bundles, Retained: svc.Retained, Outcomes: svc.Outcomes, Projects: projects})
	return NewBackups(projects, data, dir), pid, dataprotection.NewBackups(db)
}

func TestBackupListHTTPIncludesSetsPlanAndRetention(t *testing.T) {
	t.Parallel()
	h, pid, svc := backupHTTPFixture(t, "bk")
	b := &dataprotection.Backup{ProjectID: pid, Deployment: "main", TakenAt: time.Now().UTC().Format(time.RFC3339), Trigger: "manual", Actor: "bls", Path: filepath.Join(h.dataDir, "backups", "bk", "main", "one"), Status: dataprotection.BackupOK}
	if _, err := svc.Record(context.Background(), b, "bls"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/bk/deployments/main/backups", nil)
	req.SetPathValue("slug", "bk")
	req.SetPathValue("name", "main")
	w := httptest.NewRecorder()
	h.ListBackups(w, req)
	var out struct {
		Backups []dataprotection.Backup `json:"backups"`
		Plan    []map[string]string     `json:"plan"`
		Note    string                  `json:"note"`
		Keep    int                     `json:"keep"`
	}
	// The plan and why it is empty are the owner's (no standing
	// deployment here), passed through.
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Backups) != 1 || out.Plan == nil || len(out.Plan) != 0 || !strings.Contains(out.Note, "no standing deployment") || out.Keep != dataprotection.BackupKeep {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
}

func TestRestoreBackupHTTPRequiresDeploymentName(t *testing.T) {
	t.Parallel()
	h, _, _ := backupHTTPFixture(t, "rs")
	req := httptest.NewRequest("POST", "/api/v1/projects/rs/deployments/main/backups/1/restore", strings.NewReader(`{"confirm":"nope"}`))
	req.SetPathValue("slug", "rs")
	req.SetPathValue("name", "main")
	req.SetPathValue("id", "00000000-0001-7000-8000-000000000000")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.RestoreBackup(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "confirm") {
		t.Fatalf("confirm must equal the deployment name: %d %s", w.Code, w.Body.String())
	}
}
