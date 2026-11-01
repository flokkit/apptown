package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Take-down revokes the deployment's key after the target let go of the
// rest, deletes its value, and says so; the target driver is never handed
// the key. A revoke that fails is left as a retained row naming the key.
func TestTakeDownRevokesTheDeploymentsKey(t *testing.T) {
	for _, scenario := range []string{"revoked", "refused"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, _, driver, dep := removalFixture(t)
			f := withLLM(t, s.Service, dep.ProjectID, true)
			name := fmt.Sprintf("forge-config-%s-%d", dep.Name, dep.ID)
			if _, err := s.integrations.RegisterLLMKey(context.Background(), integration.KeyRequest{ProjectID: dep.ProjectID, Name: name, Scope: fmt.Sprintf("llm:deployment:%d", dep.ID)},
				ownedKeys{owner: s.Deployments.OwnerOf(dep), name: name}); err != nil {
				t.Fatal(err)
			}
			if scenario == "refused" {
				f.FailNext("DELETE", 500)
			}
			result, err := s.Remove(context.Background(), removeSelection(dep))
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range driver.got.Owned {
				if o.Kind == deployment.KindOpenRouterKey {
					t.Fatal("the target driver was handed the key")
				}
			}
			retained, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			var keyRows []deployment.RetainedResource
			for _, r := range retained {
				if r.Need == "AI models key" {
					keyRows = append(keyRows, r)
				}
			}
			switch scenario {
			case "revoked":
				if len(f.Keys()) != 0 || len(keyRows) != 0 || !strings.Contains(strings.Join(result.Deleted, ","), "AI models key forge-config-prod-") {
					t.Fatalf("revoked: keys %d, rows %+v, deleted %v", len(f.Keys()), keyRows, result.Deleted)
				}
				if _, _, err := s.secrets.Vault.Get(t.Context(), "llm:deployment:"+itoa(dep.ID), integration.LLMKeyName); !errors.Is(err, secret.ErrNotStored) {
					t.Fatalf("the stored key goes with it: %v", err)
				}
			case "refused":
				if len(f.Keys()) != 1 || len(keyRows) != 1 || keyRows[0].Handle["hash"] == "" || !strings.Contains(keyRows[0].Handle["why"].(string), "500") {
					t.Fatalf("a failed revoke is left: keys %d, rows %+v", len(f.Keys()), keyRows)
				}
				if strings.Contains(strings.Join(result.Deleted, ","), "AI models key") {
					t.Fatal("a key not revoked was called deleted")
				}
			}
		})
	}
}

// An eval copy's deployment is taken down with no final backup: its data
// is synthetic and nothing will restore it, so the data volumes it
// recorded are deleted with everything else it recorded, and the receipt
// says so.
func TestACopysTakeDownDeletesItsDataWithoutABackup(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorldFor(t, project.EvalCopy, "fbe")
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalBackup != nil {
		t.Fatalf("a final backup of synthetic data: %+v", result.FinalBackup)
	}
	cmds := commandsOf(client)
	if slices.IndexFunc(cmds, func(c string) bool { return strings.Contains(c, "c-pg sh -c pg_dump") }) >= 0 {
		t.Fatalf("the copy's data was dumped:\n%s", strings.Join(cmds, "\n"))
	}
	for _, c := range []string{"volume rm v-pg", "volume rm " + hostNamesOf(t, dep, "fbe").DataVolume()} {
		if indexOf(cmds, c) < 0 {
			t.Fatalf("%s missing:\n%s", c, strings.Join(cmds, "\n"))
		}
	}
	rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("nothing is retained: %+v %v", rows, err)
	}
}

// With a copy's last deployment go the release images AppTown published
// under the copy's own repository, and nothing published elsewhere.
func TestACopysImagesGoWithItsLastDeployment(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	copy := createProjectFor(t, db, "shop-copy", "Shop copy", project.EvalCopy)
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, err := integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"`+host+`","group":"evals"}`, false); err != nil {
		t.Fatal(err)
	}
	s := assembled(db, testRuntime{DataDir: dir, Projects: projects, Integrations: integrations}, deployment.Dependencies{})
	push := func(repository string) string {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := name.ParseReference(repository + ":t")
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		digest, _ := img.Digest()
		return repository + "@" + digest.String()
	}
	own, elsewhere := push(host+"/evals/shop-copy"), push(host+"/team/real")
	for n, ref := range map[uint64]string{1: own, 2: elsewhere} {
		run := idstest.N(n)
		repository, _, _ := strings.Cut(ref, "@")
		origin := deployment.BundleOrigin{Registry: repository, Platform: "linux/amd64", Build: deployment.BundleBuild{Worker: "http://worker:7070"},
			By: &claimedAttempt{db: db, project: copy, run: run, step: idstest.N(n * 10), at: 1}}
		if _, err := s.Bundles.Cut(t.Context(), copy, "tree", map[string]string{"web": ref}, nil, []byte("app: {}\n"), origin); err != nil {
			t.Fatal(err)
		}
	}
	var taken deploy.TakenDown
	s.deleteCopyImages(context.Background(), copy, &taken)
	if len(taken.Left) != 0 || len(taken.Deleted) != 1 {
		t.Fatalf("taken down: %+v", taken)
	}
	for ref, gone := range map[string]bool{own: true, elsewhere: false} {
		_, err := remote.Head(mustDigest(t, ref))
		if gone == (err == nil) {
			t.Errorf("%s: gone=%v wanted, head error %v", ref, gone, err)
		}
	}
}

// Backups of an attached target keep working in the mode, on its daemon;
// the built-in host's are unavailable.
func TestNoLocalDockerBacksUpAttachedTargetsOnly(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, "app:\n  web:\n    image: example/game:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n")
	s := w.service(true)
	dep := w.deployment(t, s, "main", w.remote)
	runRelease(t, s, dep, w.revision, "")
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	w.daemon.SetVolume(names.DataVolume(), true)
	local, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	w.deployment(t, s, "old", local)
	w.daemon.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		if req.Stdout == nil {
			return nil, nil, false
		}
		_, err := containerresulttest.WriteString(req.Stdout, "data")
		return nil, err, true
	})
	b, err := s.TakeBackup(context.Background(), w.project, "main", "alice")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 {
		t.Fatalf("a backup of the attached target: %+v %v", b, err)
	}
	if other := w.daemon.otherHosts(); len(other) != 0 {
		t.Fatalf("the backup asked another daemon: %v", other)
	}
	w.daemon.ResetCommands()
	if _, err := s.TakeBackup(context.Background(), w.project, "old", "alice"); !errors.Is(err, deployment.ErrNoLocalDocker) {
		t.Fatalf("a backup on the built-in host: %v", err)
	}
	if len(w.daemon.Commands()) != 0 {
		t.Fatal("the built-in host's backup reached a daemon")
	}
}

// backupsWhere is every backup set on the instance that satisfies keep. A
// full page fails the test, so the listing is every set.
func backupsWhere(t *testing.T, db *database.DB, keep func(ProjectBackup) bool) []ProjectBackup {
	t.Helper()
	const limit = 1000
	all, err := NewBackups(db).All(context.Background(), limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) >= limit {
		t.Fatalf("%d backups: the listing may be incomplete", len(all))
	}
	var out []ProjectBackup
	for _, b := range all {
		if keep(b) {
			out = append(out, b)
		}
	}
	return out
}

func backupIn(state BackupState) func(ProjectBackup) bool {
	return func(b ProjectBackup) bool { return b.Status == state }
}
