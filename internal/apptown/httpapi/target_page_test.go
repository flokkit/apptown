package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
)

func TestTargetCheckAndRetire(t *testing.T) {
	t.Parallel()
	w := newTargetHTTPWorld(t)
	w.seedBuiltin(t)
	call := func(handler http.HandlerFunc, method, path, body, name string) (int, map[string]any) {
		return targetCall(t, handler, method, path, body, map[string]string{"name": name})
	}
	w.docker.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "info" {
			return []byte("29.0 3/4 containers\n"), nil, true
		}
		return nil, nil, false
	})
	code, out := call(w.targets.CheckTarget, "POST", "/api/v1/targets/"+targetTestBuiltin+"/check", "", targetTestBuiltin)
	if code != 200 || out["ok"] != true || out["status"] != deployment.TargetReady.String() {
		t.Fatalf("built-in check: %d %v", code, out)
	}
	if code, out := call(w.targets.RetireTarget, "POST", "/api/v1/targets/"+targetTestBuiltin+"/retire", `{"confirm":"`+targetTestBuiltin+`"}`, targetTestBuiltin); code != 409 || !strings.Contains(out["error"].(string), "cannot be retired") {
		t.Fatalf("built-in retirement: %d %v", code, out)
	}
	w.docker.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "info" {
			return nil, errors.New("daemon unavailable"), true
		}
		return nil, nil, false
	})
	if code, out := call(w.targets.CheckTarget, "POST", "/api/v1/targets/"+targetTestBuiltin+"/check", "", targetTestBuiltin); code != 200 || out["ok"] != false || out["status"] != deployment.TargetUnreachable.String() {
		t.Fatalf("unreachable: %d %v", code, out)
	}

	target, err := w.deployment.Targets.Put(context.Background(), &deployment.Target{Name: "spare-box", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, NeedsMode: "emulators", Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	pid := w.createProject(t, "tp")
	if _, err := w.deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if err := w.deployment.Targets.Offer(context.Background(), target, "*", deployment.OfferCaps{}, "t"); err != nil {
		t.Fatal(err)
	}
	code, out = call(w.targets.TargetPage, "GET", "/api/v1/targets/spare-box", "", "spare-box")
	if code != 200 {
		t.Fatalf("bound target read: %d %v", code, out)
	}
	placements := out["target"].(map[string]any)["deployments"].([]any)
	if len(placements) != 1 {
		t.Fatalf("target deployments: %+v", placements)
	}
	code, out = call(w.targets.RetireTarget, "POST", "/api/v1/targets/spare-box/retire", `{"confirm":"spare-box"}`, "spare-box")
	if code != 409 || !strings.Contains(out["error"].(string), "tp/main") {
		t.Fatalf("bound retirement: %d %v", code, out)
	}
	if err := w.db.InTx(context.Background(), func(tx *database.Tx) error { return deployment.RemoveProjectIn(context.Background(), tx, pid) }); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(w.targets.RetireTarget, "POST", "/api/v1/targets/spare-box/retire", `{"confirm":"nope"}`, "spare-box"); code != 400 {
		t.Fatalf("wrong confirmation: %d", code)
	}
	if code, out := call(w.targets.RetireTarget, "POST", "/api/v1/targets/spare-box/retire", `{"confirm":"spare-box"}`, "spare-box"); code != 200 || out["status"] != deployment.TargetRetired.String() {
		t.Fatalf("retire: %d %v", code, out)
	}
	targetOffersAre(t, w, target.ID, 0)
	code, out = call(w.targets.TargetPage, "GET", "/api/v1/targets/spare-box", "", "spare-box")
	history, _ := out["history"].([]any)
	if code != 200 || len(history) == 0 {
		t.Fatalf("history: %d %v", code, out)
	}
	if code, _ := call(w.targets.CheckTarget, "POST", "/api/v1/targets/spare-box/check", "", "spare-box"); code != 409 {
		t.Fatalf("retired check: %d", code)
	}
}

func TestRemoveUnrecordedInfrastructure(t *testing.T) {
	t.Parallel()
	w := newTargetHTTPWorld(t)
	w.docker.SetVolume("apptown-data-apptown-env-old-main", true)
	w.docker.SetRunning("apptown-dep-orphan", true)
	// This fixture's fake supports label-filtered ps, while inventory asks
	// name filters and volume listings. Supply the seeded orphan observations and
	// retain the existing daemon for removal and command evidence.
	w.setDockerClient(targetDockerClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if len(req.Args) > 1 && req.Args[0] == "ps" && req.Args[1] == "-a" {
			if w.docker.Running("apptown-dep-orphan") {
				return []byte("apptown-dep-orphan\tUp 1 hour\tsha256:orphan\t\t\t" + testInstallation + "\n"), nil
			}
			return nil, nil
		}
		if len(req.Args) > 1 && req.Args[0] == "volume" && req.Args[1] == "ls" {
			return []byte("apptown-data-apptown-env-old-main\t\t\t" + testInstallation + "\n"), nil
		}
		return w.docker.Run(ctx, host, req)
	}))
	builtin := w.seedBuiltin(t)
	pid := w.createProject(t, "ri")
	env, err := w.deployment.Deployments.EnsureStanding(t.Context(), pid, "main", builtin.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// A data volume the deployment recorded making is its own, by whatever
	// name: the infrastructure page never removes it by hand.
	if err := w.deployment.Deployments.OwnerOf(env).Created(context.Background(), deploy.Owned{Kind: "volume", ID: "apptown-need-env-ri-main-postgres", Data: true, Need: "postgres"}); err != nil {
		t.Fatal(err)
	}
	call := func(body string) (int, map[string]any) {
		return targetCall(t, w.infra.RemoveInfrastructure, "POST", "/api/v1/infrastructure/"+targetTestBuiltin+"/remove", body, map[string]string{"target": targetTestBuiltin})
	}
	names, err := env.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	app := names.App() // what the deployment's app is called on its host, recorded or not
	if code, out := call(fmt.Sprintf(`{"kind":"container","name":%q,"confirm":%q}`, app, app)); code != 409 || !strings.Contains(out["error"].(string), "ri/main") {
		t.Fatalf("recorded container: %d %v", code, out)
	}
	if code, out := call(`{"kind":"volume","name":"apptown-need-env-ri-main-postgres","confirm":"apptown-need-env-ri-main-postgres"}`); code != 409 || !strings.Contains(out["error"].(string), "ri/main") {
		t.Fatalf("a recorded data volume: %d %v", code, out)
	}
	// Display normalization must not strip immutable IDs from the claims
	// used by this consequential door.
	if err := w.deployment.Deployments.OwnerOf(env).Created(t.Context(), deploy.Owned{Kind: "container", ID: "immutable-incarnation", Name: app}); err != nil {
		t.Fatal(err)
	}
	if code, out := call(`{"kind":"container","name":"alias","id":"immutable-incarnation","confirm":"alias"}`); code != 409 {
		t.Fatalf("recorded immutable container: %d %v", code, out)
	}
	// Records that cannot be read claim nothing and permit nothing:
	// the removal is refused before Docker is asked to delete anything.
	w.docker.ResetCommands()
	restore := dbtest.NextStatementFailure(w.db, "-- name: OwnedByDeployment")
	if code, out := call(`{"kind":"volume","name":"apptown-need-env-ri-main-postgres","confirm":"apptown-need-env-ri-main-postgres"}`); code != 503 || !strings.Contains(out["error"].(string), "records could not be read") {
		t.Fatalf("a removal with its records unread: %d %v", code, out)
	}
	restore()
	if len(w.docker.Commands()) != 0 {
		t.Fatalf("a removal with its records unread reached Docker: %v", w.docker.Commands())
	}
	restore = dbtest.NextStatementFailure(w.db, "-- name: OwnedByDeployment")
	if code, _ := targetCall(t, w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil); code == 200 {
		t.Fatal("the infrastructure page read its records as complete when they could not be read")
	}
	restore()
	if code, _ := call(`{"kind":"container","name":"apptown-dep-orphan","confirm":"other"}`); code != 400 {
		t.Fatalf("confirmation: %d", code)
	}
	// A removal cannot be undone: without its request on the record, Docker
	// is not asked.
	w.docker.ResetCommands()
	restore = dbtest.NextStatementFailure(w.db, "-- name: AppendReceipt")
	code, out := call(`{"kind":"container","name":"apptown-dep-orphan","confirm":"apptown-dep-orphan"}`)
	restore()
	removed := false
	for _, command := range w.docker.Commands() {
		if len(command) > 0 && command[0] == "rm" {
			removed = true
		}
	}
	if code != 503 || removed {
		t.Fatalf("a removal whose request was not recorded: %d %v, docker %v", code, out, w.docker.Commands())
	}
	if code, out := call(`{"kind":"container","name":"apptown-dep-orphan","confirm":"apptown-dep-orphan"}`); code != 200 {
		t.Fatalf("unrecorded container: %d %v", code, out)
	}
	if got, err := audit.Recent(t.Context(), w.db, audit.Filter{KindPrefix: "infrastructure.remove"}); err != nil || len(got) != 2 || got[0].Kind != "infrastructure.removed" || got[1].Kind != "infrastructure.remove-requested" {
		t.Fatalf("a removal's request and outcome: %+v %v", got, err)
	}
	if code, out := call(`{"kind":"volume","name":"apptown-data-apptown-env-old-main","confirm":"apptown-data-apptown-env-old-main"}`); code != 200 {
		t.Fatalf("unrecorded volume: %d %v", code, out)
	}
	var calls []string
	for _, args := range w.docker.Commands() {
		calls = append(calls, strings.Join(args, " "))
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "rm -f apptown-dep-orphan") || !strings.Contains(joined, "volume rm apptown-data-apptown-env-old-main") {
		t.Fatalf("docker removal:\n%s", joined)
	}
}
