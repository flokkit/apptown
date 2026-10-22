package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec"
)

func TestBackupHTTPCommandsRequireAuthorityAndConfirmedScopedRestore(t *testing.T) {
	t.Parallel()
	f := newFakeDocker()
	a := workAuthorityFixture(t, Config{DockerClient: f})
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "backup-host", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// main runs a committed contract that keeps data: what it holds is read
	// from the release it runs.
	ws := filepath.Join(a.cfg.dataDir, "workspaces", "project-game")
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web:\n    image: example/game:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "runtime contract"}} {
		if _, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatal(err)
		}
	}
	head, err := gitexec.Run(t.Context(), ws, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	at, _, err := a.Deployment.Outcomes.Admit(context.Background(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "release-1", Inputs: deployment.Inputs{Revision: strings.TrimSpace(string(head))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	// main's app runs, so a restore stops it and starts it again.
	f.markStarted(docker.ByKey(dep.ID).App())
	// The host holds main's data volume, as its boot made it.
	if _, err := f.run("volume", "create", docker.ByKey(dep.ID).DataVolume()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var copied, restored []string
	f.command = func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		calls++
		if req.Stdout != nil {
			copied = append(copied, fmt.Sprintf("saved game %d", calls))
			_, err := io.WriteString(req.Stdout, copied[len(copied)-1])
			return nil, err
		}
		if req.Stdin != nil {
			raw, err := io.ReadAll(req.Stdin)
			restored = append(restored, string(raw))
			return nil, err
		}
		return f.run(req.Args...)
	}
	h := a.Handler()
	base := "/api/v1/projects/game/deployments/main/backups"
	if w := authzCall(h, "outsider", "POST", base, ""); w.Code != 403 || calls != 0 {
		t.Fatalf("denied backup: %d %s calls=%d", w.Code, w.Body.String(), calls)
	}
	w := authzCall(h, "boss", "POST", base, "")
	var taken struct {
		OK     bool
		Backup dataprotection.Backup
	}
	if err := json.Unmarshal(w.Body.Bytes(), &taken); err != nil || w.Code != 200 || !taken.OK || taken.Backup.ID.IsZero() || taken.Backup.ProjectID != idstest.N(1) || taken.Backup.Actor != "boss" || taken.Backup.Trigger != "manual" || taken.Backup.Status != dataprotection.BackupOK {
		t.Fatalf("backup receipt: %d %s %v", w.Code, w.Body.String(), err)
	}
	source := taken.Backup.ID
	path := fmt.Sprintf("%s/%d/restore", base, source)
	before := calls
	for _, tc := range []struct {
		who, path, body string
		code            int
	}{
		{"outsider", path, `{"confirm":"main"}`, 403},
		{"boss", path, `{"confirm":"nope"}`, 400},
		{"boss", path, `{"confirm":"main","confirm":false}`, 400},
		{"boss", strings.Replace(path, "game", "open", 1), `{"confirm":"main"}`, 404},
	} {
		w := authzCall(h, tc.who, "POST", tc.path, tc.body)
		if w.Code != tc.code || calls != before {
			t.Fatalf("refused restore: %d %s calls %d -> %d", w.Code, w.Body.String(), before, calls)
		}
	}
	// The restore is an operation: the answer is the operation, which runs
	// once and keeps the receipt in its result.
	w = authzCall(h, "boss", "POST", path, `{"confirm":" main "}`)
	var admitted httpapi.OperationAdmitted
	if err := json.Unmarshal(w.Body.Bytes(), &admitted); err != nil || w.Code != 202 || admitted.OperationID.IsZero() {
		t.Fatalf("restore admitted: %d %s %v", w.Code, w.Body.String(), err)
	}
	op, err := a.operations.RunHere(context.Background(), admitted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(op.Result["restore"])
	var receipt struct {
		Backup, SafetyBackup ids.ID
		Status               string
		Items                []struct{ Outcome, Detail string }
	}
	// The app's data volume goes back as files nothing checks: the
	// receipt says so rather than "restored".
	if err := json.Unmarshal(raw, &receipt); err != nil || op.State != operations.Succeeded || receipt.Backup != source || receipt.SafetyBackup.Compare(source) <= 0 || receipt.Status != "restored, not verified" || len(receipt.Items) != 1 || receipt.Items[0].Outcome != "copied" || !strings.Contains(receipt.Items[0].Detail, "not verified") || len(restored) != 1 || restored[0] != copied[0] {
		t.Fatalf("restore receipt/bytes: %s %q %s restored=%v %v", op.State, op.Detail, raw, restored, err)
	}
	sets, err := a.DataProtection.DeploymentSets(context.Background(), idstest.N(1), "main", 50)
	if err != nil || len(sets) != 2 || sets[0].ID != receipt.SafetyBackup || sets[0].Trigger != dataprotection.TriggerPreRestore || sets[0].Actor != "boss" {
		t.Fatalf("safety set: %+v %v", sets, err)
	}
	w = authzCall(h, "boss", "GET", "/api/v1/backups", "")
	var all struct {
		Backups []dataprotection.ProjectBackup
	}
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil || w.Code != 200 || len(all.Backups) != 2 || all.Backups[0].ID != receipt.SafetyBackup {
		t.Fatalf("fleet backup receipt: %d %s %v", w.Code, w.Body.String(), err)
	}
}
