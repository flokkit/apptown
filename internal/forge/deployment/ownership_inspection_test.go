package deployment

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/stock"
)

// A deployment made before AppTown recorded its creations has no list.
// The inspection proposes what the host holds under its names and labels,
// records nothing on its own, and records only what a person approves,
// with the approval as its note; a key it does not propose now is refused.
func TestTheOwnershipInspectionProposesAndRecordsOnlyWhatIsApproved(t *testing.T) {
	t.Parallel()
	s, client, pid := inspectionWorld(t, "oi", 40200)
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "staging", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	id := func(c string) string { return c + strings.Repeat("0", 64-len(c)) }
	client.SetReply(func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "inspect -f {{.Id}} {{index .Config.Labels \"forge.environment\"}} apptown-env-oi-staging"):
			return []byte(id("a") + " " + dep.ID.String()), nil, true
		case strings.HasPrefix(line, "inspect -f {{.Id}} {{index .Config.Labels \"forge.environment\"}} apptown-env-oi-main"):
			// A container under main's name that another deployment's
			// identity labels: not main's.
			return []byte(id("d") + " 999"), nil, true
		case strings.HasPrefix(line, "ps -aq --no-trunc") && strings.Contains(line, "label=forge.owner=env-oi-staging"):
			return []byte(id("b") + "\n" + id("c") + "\n"), nil, true
		case line == "inspect -f {{.Name}} "+id("b"):
			return []byte("/apptown-svc-env-oi-staging-postgres\n"), nil, true
		case line == "inspect -f {{.Name}} "+id("c"):
			return []byte("/apptown-svc-env-oi-staging-worker\n"), nil, true
		case strings.Contains(line, ".Mounts") && strings.HasSuffix(line, id("b")):
			return []byte("anon-pg\t/var/lib/postgresql/data\n"), nil, true
		case strings.HasPrefix(line, "volume ls"):
			return nil, nil, true
		case line == "volume inspect apptown-data-apptown-env-oi-staging", line == "network inspect apptown-net-env-oi-staging":
			return nil, nil, true
		case strings.HasPrefix(line, "volume inspect"), strings.HasPrefix(line, "network inspect"):
			return []byte("Error response from daemon: No such object"), errors.New("exit 1"), true
		}
		return nil, nil, false
	})
	ctx := context.Background()
	inspection, err := s.InspectOwnership(ctx)
	if err != nil || len(inspection.Unknown) != 0 {
		t.Fatalf("inspection: %+v %v", inspection, err)
	}
	var got []string
	keys := map[string]string{}
	for _, p := range inspection.Proposals {
		if p.Deployment == "main" && p.Owned.Kind == "container" {
			t.Fatalf("a container labelled with another identity was proposed: %+v", p)
		}
		if p.Deployment != "staging" {
			continue
		}
		label := p.Owned.Label()
		if p.Owned.Need != "" {
			label += " for " + p.Owned.Need
		}
		got = append(got, label)
		keys[label] = p.Key
	}
	slices.Sort(got)
	want := []string{"container apptown-env-oi-staging", "container apptown-svc-env-oi-staging-postgres for postgres", "container apptown-svc-env-oi-staging-worker", "network apptown-net-env-oi-staging", "volume anon-pg for postgres", "volume apptown-data-apptown-env-oi-staging"}
	if !slices.Equal(got, want) {
		t.Fatalf("proposed\n%v\nwant\n%v", got, want)
	}
	if owned, _ := s.Deployments.Owned(ctx, dep.ID); len(owned) != 0 {
		t.Fatalf("the inspection recorded on its own: %+v", owned)
	}
	if _, err := s.ApproveOwnership(ctx, []string{keys["volume anon-pg for postgres"], "deployment:999:volume:made-up"}, "bls"); !errors.Is(err, ErrNotProposed) {
		t.Fatalf("an unproposed key was approved: %v", err)
	}
	if owned, _ := s.Deployments.Owned(ctx, dep.ID); len(owned) != 0 {
		t.Fatalf("a refused approval recorded: %+v", owned)
	}
	approved, err := s.ApproveOwnership(ctx, []string{keys["volume anon-pg for postgres"], keys["container apptown-svc-env-oi-staging-postgres for postgres"]}, "bls")
	if err != nil || len(approved) != 2 {
		t.Fatalf("approval: %+v %v", approved, err)
	}
	owned, err := s.Deployments.Owned(ctx, dep.ID)
	if err != nil || len(owned) != 2 || !slices.ContainsFunc(owned, func(o deploy.Owned) bool { return o.ID == "anon-pg" && o.Data && o.Need == "postgres" }) {
		t.Fatalf("recorded: %+v %v", owned, err)
	}
	again, err := s.InspectOwnership(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range again.Proposals {
		if p.Key == keys["volume anon-pg for postgres"] {
			t.Fatal("an approved entry is proposed again")
		}
	}
	receipts, err := audit.Recent(ctx, s.db, audit.Filter{ProjectID: pid, KindPrefix: "ownership.approved"})
	if err != nil || len(receipts) != 2 || receipts[0].Actor != "bls" {
		t.Fatalf("receipts: %+v %v", receipts, err)
	}
}

// inspectionWorld is a project on this AppTown's own Docker (a fake daemon)
// with its main deployment bound there at port.
func inspectionWorld(t *testing.T, slug string, port int) (*Service, *dockerfake.Daemon, ids.ID) {
	t.Helper()
	db := testDB(t)
	pid := createTestProject(t, db, slug, strings.ToUpper(slug))
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	client := dockerfake.New()
	s := New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: client}, DataDir: t.TempDir(), Needs: catalog, Projects: project.New(db, nil), BuiltInTarget: "local"}, Dependencies{}))
	target, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), pid, "main", docker.Current.Container(slug, "main"), port); err != nil {
		t.Fatal(err)
	}
	return s, client, pid
}
