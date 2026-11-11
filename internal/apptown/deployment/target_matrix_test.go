package deployment

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// matrixRow is one row of deploy-targets.md, "What each target supports".
// cells are the five columns as the guide shows them (docker-host,
// portainer, do-app, fly-machines, s3-bucket). A row the admission checks decide also
// carries a contract (and, for image rows, a bundle) and, per kind, ""
// for supported or a fragment of the refusal. pinnedBy names the tests
// that own the row's guarantee through its real operation (ordering
// before effects, witnessed outcomes); the guide lists them.
type matrixRow struct {
	row      string
	cells    [6]string
	app      string
	bundle   *ReleaseBundle
	want     map[string]string
	pinnedBy []string
}

var matrixKinds = [6]string{KindDockerHost, KindPortainer, KindDOApp, kindFlyMachines, KindS3Bucket, KindAWSECS}

var targetMatrix = []matrixRow{
	{row: "Several parts, one image",
		cells:    [6]string{"yes", "yes", "yes", "yes", "refused: serves only a static-only app", "refused: single-part apps"},
		app:      "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\n  worker:\n    image: reg/x:1\n    command: [\"work\"]\n",
		bundle:   &ReleaseBundle{ID: idstest.N(1), Parts: map[string]string{"web": "reg/x:1", "worker": "reg/x:1"}},
		want:     map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "", kindFlyMachines: "", KindS3Bucket: "static-only", KindAWSECS: "single-part"},
		pinnedBy: []string{"TestABucketTargetServesOnlyAStaticOnlyApp"}},
	{row: "Parts or init steps with different images",
		cells:    [6]string{"yes", "yes", "refused: App Platform runs one image per repository", "yes", "refused: static-only", "refused: single-part apps"},
		app:      "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\n  worker:\n    image: reg/y:1\n    command: [\"work\"]\n",
		bundle:   &ReleaseBundle{ID: idstest.N(2), Parts: map[string]string{"web": "reg/x:1", "worker": "reg/y:1"}},
		want:     map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "one image per repository", kindFlyMachines: "", KindS3Bucket: "static-only", KindAWSECS: "single-part"},
		pinnedBy: []string{"TestAMultipartBundleOfDistinctImagesIsRefusedBeforeTheAppPlatformApp"}},
	{row: "Init steps",
		cells: [6]string{"yes", "yes", "yes, as pre-deploy jobs", "yes, as one-off Machines before any part's", "refused: static-only", "yes, as one-off tasks before the service changes"},
		app:   "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\ninit:\n  - {name: migrate, image: reg/x:1, command: [\"migrate\"]}\n",
		want:  map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "", kindFlyMachines: "", KindS3Bucket: "static-only", KindAWSECS: ""}},
	{row: "Postgres",
		cells: [6]string{"yes, a local emulator", "yes, a local emulator", "yes, a managed database", "yes, a stock container on its own volume", "refused: static-only", "yes, a dedicated RDS instance"},
		app:   "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n",
		want:  map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "", kindFlyMachines: "", KindS3Bucket: "static-only", KindAWSECS: ""}},
	{row: "Object storage (s3)",
		cells:    [6]string{"yes, a local store: its bucket made before the app starts, a key for it alone", "yes, a local store, as on a Docker host", "yes, a Spaces bucket of its own and a key for it alone", "refused: cannot run as a need container", "refused: static-only", "refused: not offered yet"},
		app:      "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  s3: {}\n",
		want:     map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "", kindFlyMachines: "cannot run as a need container", KindS3Bucket: "static-only", KindAWSECS: "cannot provide as a managed service"},
		pinnedBy: []string{"TestAnS3NeedOnAppPlatformIsABucketAndAKeyForItAlone"}},
	{row: "Another need with a local emulator (redis)",
		cells:    [6]string{"yes, a local emulator", "yes, a local emulator", "refused: cannot provide it as a managed service", "refused: cannot run as a need container", "refused: static-only", "refused: not offered yet"},
		app:      "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  redis: {}\n",
		want:     map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "cannot provide as a managed service", kindFlyMachines: "cannot run as a need container", KindS3Bucket: "static-only", KindAWSECS: "cannot provide as a managed service"},
		pinnedBy: []string{"TestTargetRefusesContract"}},
	{row: "A data directory",
		cells:    [6]string{"yes, a named volume", "yes, a named volume", "refused: no persistent volumes", "yes, a named volume; stop then start on release", "refused: static-only", "refused: no data directories"},
		app:      "app:\n  web:\n    image: reg/x:1\n    ports: [\"8080\"]\n    public: true\n    dataDir: /data\n",
		want:     map[string]string{KindDockerHost: "", KindPortainer: "", KindDOApp: "no persistent volumes", kindFlyMachines: "", KindS3Bucket: "static-only", KindAWSECS: "no persistent volumes"},
		pinnedBy: []string{"TestTargetRefusesContract", "TestRequestReleaseRefusesUnreadableOrIncompatibleContract"}},
	{row: "A static site given browser values by an integration",
		cells:    [6]string{"yes, as /_apptown/env.js", "yes, as /_apptown/env.js", "refused: cannot deliver browser values yet", "refused: cannot deliver browser values yet", "yes, published as /_apptown/env.js", "refused: cannot deliver browser values yet"},
		pinnedBy: []string{"TestRequestReleaseRefusesUnreadableOrIncompatibleContract", "TestAPortainerStackGivesTheAppItsIntegrationValues"}},
	{row: "Secrets",
		cells: [6]string{"each part's environment", "each part's environment", "each component's environment", "the app's secrets, the same for every Machine; parts or init steps given different ones are refused before any effect", "none: a site has no runtime", "SSM SecureString values, immutable per generation and consumer"}},
	{row: "Declared resources",
		cells:    [6]string{"exact: each part's container gets its memory (swap included) and cpus", "exact: each part's service gets mem_limit, memswap_limit and cpus", "rounded up to the cheapest current instance size that covers each part; one no size covers is refused before any effect", "rounded up to the smallest shared-CPU Machine that covers each part; one none covers is refused before any effect", "none: a site has no runtime", "rounded up to the smallest Fargate CPU and memory pair"},
		pinnedBy: []string{"TestAPartsContainerIsHeldToItsDeclaredResources", "TestAnIntranetStackHoldsEachPartToItsDeclaredResources", "TestAppPlatformFitsEachPartBeforeAnyEffect"}},
	{row: "A need's declared resources",
		cells:    [6]string{"exact on the need's service; a standing need takes a new size in place, or is remade on its volume when a limit is lifted", "exact: the need's service gets mem_limit, memswap_limit and cpus", "refused: the managed Postgres is made at the integration's database size", "rounded up to a shared-CPU Machine; the standing need is updated in place on its volume", "refused: static-only", "refused: fixed dedicated RDS class"},
		pinnedBy: []string{"TestANeedsServiceIsHeldToItsDeclaredSize", "TestAStandingNeedTakesItsNewSizeWithoutLosingItsData", "TestAnIntranetStackHoldsEachPartToItsDeclaredResources", "TestAnAppPlatformPartNoSizeCoversIsRefusedBeforeAnyEffect"}},
	{row: "A part or need larger than the host",
		cells:    [6]string{"refused before any effect, previews included; the sum of limits may exceed the host", "not checked: Portainer's endpoint is read for the capacity view only", "rounds to a size, or is refused", "rounds to a Machine size, or is refused", "none: a site has no runtime", "rounds to a Fargate size, or is refused"},
		pinnedBy: []string{"TestAPartOrNeedLargerThanTheHostIsRefused", "TestAReleaseLargerThanItsHostIsRefusedBeforeAnyEffect", "TestAnOversizedPreviewLeavesTheRunningOneAlone", "TestAWorkerRefusesAnOversizedPreviewBeforeRemovingTheRunningOne"}},
	{row: "Capacity shown",
		cells:    [6]string{"fixed: memory and CPUs it has, the limits Docker enforces, whole-host memory and CPU use, the containers' use, and the disk holding Docker's data (its folder, when it was read, and whether new releases are refused)", "fixed: as a Docker host, read through the endpoint's Docker proxy", "elastic: each part's size and listed price, and an estimated monthly cost", "elastic: each part's Machine size; prices are not read, Fly lists none AppTown can ask", "none: a site has no runtime", "elastic: each part’s Fargate size; cost not read"},
		pinnedBy: []string{"TestAHostsCapacityIsWhatItsDaemonReports", "TestAPortainerTargetsCapacityIsWhatItsDaemonReports", "TestAnElasticTargetShowsSizesAndAnEstimate"}},
	{row: "A host short of disk",
		cells:    [6]string{"a release or preview is refused before anything is pulled or started while Docker's data folder has less than 5% free (at least 1 GiB, at most 20 GiB) or cannot be read, read when it starts; what runs is not touched", "as a Docker host: a release is refused before any stack is sent while the endpoint's Docker data folder has less than 5% free (at least 1 GiB, at most 20 GiB) or cannot be read, read through Portainer when it starts; what runs is not touched", "not applicable: the platform's disks", "not applicable: the platform's disks", "not applicable", "not applicable: the platform’s disks"},
		pinnedBy: []string{"TestAReleaseToAHostShortOfDiskChangesNothingThere", "TestABootIsRefusedOnADaemonShortOfDisk", "TestAPortainerReleaseToAnEndpointShortOfDiskSendsNoStack"}},
	{row: "Container logs",
		cells:    [6]string{"every container AppTown starts from this release on logs through `json-file`, rotated at 10 MiB, three files kept; one started earlier keeps its own until it is made again", "the endpoint daemon's default; not set by AppTown in 1.0", "the platform's", "the platform's", "none: a site has no runtime", "CloudWatch log group per deployment, retained 14 days"},
		pinnedBy: []string{"TestEveryContainerAppKindKeepsABoundedLog", "TestIntegrationAStandingContainersLogIsRotated"}},
	{row: "Limit events",
		cells:    [6]string{"out-of-memory kills and exits of every standing deployment's parts and needs, from the daemon's events", "not read", "not reported to AppTown", "not reported to AppTown", "none: a site has no runtime", "stopped task reason, including out of memory"},
		pinnedBy: []string{"TestLimitEventsAreRecordedForEveryStandingDeployment"}},
	{row: "Releases after a landing",
		cells:    [6]string{"each follows-main deployment requests the landing’s bundle", "follows-main or manual release of a digest bundle", "as App Platform", "as App Platform", "as App Platform", "as App Platform"},
		pinnedBy: []string{"TestWorkerBuildDoesNotNeedAnAvailableDeployment"}},
	{row: "Readiness witnessed",
		cells:    [6]string{"the container AppTown started; a static site's release identity", "the stack was accepted; health unknown", "the platform's deployment is active (its own probe); AppTown asks the front door only for a declared HTTP readiness path or a static site's release identity", "each new Machine is started and passes its checks; AppTown asks the front door for a declared HTTP readiness path and a static site's release identity", "the published files answer this release's identity", "every running task is on the recorded revision and the target is healthy; the declared front-door path or static identity answers"},
		pinnedBy: []string{"TestAppPlatformAsksTheFrontDoorOnlyWhenTheContractSaysWhat"}},
	{row: "An earlier release again",
		cells: [6]string{"yes, its bundle by digest; data is not rolled back", "yes; data is not rolled back", "yes; data is not rolled back", "yes, its images copied again by digest; data is not rolled back", "yes", "yes, its images copied by digest; data is not rolled back"}},
	{row: "Take-down",
		cells:    [6]string{"what AppTown recorded creating; its data volumes are deleted after a complete final backup, refused without one; what it did not create is named and kept", "the stack AppTown recorded, then the volumes its deploys made after a complete final backup, refused without one unless a person chooses to keep the data; a volume it did not make is named and kept", "the app and managed resources AppTown recorded; a need's bucket is deleted with its objects after a checked final copy or when its data is synthetic, otherwise kept and named, and its key deleted", "the recorded Fly app and volumes after a complete final set; without a store, refused unless a person chooses to keep the app and volumes with all Machines stopped", "the bucket AppTown recorded", "the recorded service and resources; dedicated RDS and its network kept without a final checked set"},
		pinnedBy: []string{"TestTakeDownBacksUpWhatTheDeploymentHoldsThenDeletesIt"}},
	{row: "Volume backups, restored in place",
		cells:    [6]string{"yes, each data volume and need", "yes, each data volume and need, through Portainer's Docker proxy (an edge environment in async mode is refused)", "no volumes", "yes, in the backup store: declared need exports and stop-copy-start volume tars; database restore in place and files into a new volume", "no data", "no volumes"},
		pinnedBy: []string{"TestTargetHoldsBackups"}},
	{row: "Managed database copies",
		cells:    [6]string{"not applicable", "not applicable", "yes, with a copy runner and backup store; restored into a new place, never in place", "not applicable: Postgres is a container on a volume", "not applicable", "yes, one-off ECS tasks into the backup store; every database dumped or shown empty, with rdsadmin explicitly provider-owned"},
		pinnedBy: []string{"TestAStoreCopyIsNeverRestoredInPlace"}},
}

// renderTargetMatrix is the guide's table, exactly.
func renderTargetMatrix() string {
	var b strings.Builder
	b.WriteString("| Contract or act | `docker-host` | `portainer` | `do-app` | `fly-machines` | `s3-bucket` | `aws-ecs` |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range targetMatrix {
		b.WriteString("| " + r.row + " | " + strings.Join(r.cells[:], " | ") + " |\n")
	}
	return b.String()
}

// Each target refuses what it cannot run before any effect, in the words
// of the guide's table, and runs the rest. The rows the admission checks
// decide are driven through the checks the release path calls before
// anything is prepared (RefusesContract, RefusesBundle), for a target of each kind with its stock capabilities;
// the guide's table is this test's rows, exactly; and every test a row
// names as its owner exists.
//
// Kills: a capability changed in the code and not in the table; a cell
// moved, swapped or reworded in the guide and not here; a row's owning
// test renamed or removed.
func TestEachTargetRefusesWhatItCannotRunBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	targets := map[string]*Target{}
	for _, kind := range matrixKinds {
		targets[kind] = &Target{Name: "the " + kind + " target", Kind: kind, Capabilities: stockCapabilities(kind, []string{"postgres", "redis", "s3"}), NeedsMode: stockNeedsMode(kind)}
	}
	providers := &Providers{hosts: &Hosts{}}
	for _, r := range targetMatrix {
		if r.app == "" {
			continue
		}
		c, err := appcontract.Parse([]byte(r.app))
		if err != nil {
			t.Fatalf("%s: %v", r.row, err)
		}
		for i, kind := range matrixKinds {
			want, cell := r.want[kind], r.cells[i]
			if (want == "") != !strings.HasPrefix(cell, "refused") {
				t.Errorf("%s on %s: the check expects %q and the table says %q", r.row, kind, want, cell)
			}
			tg := targets[kind]
			why := tg.refusesContract(c)
			if why == "" && r.bundle != nil && kind == KindDOApp {
				if err := providers.RefusesBundle(context.Background(), r.bundle, tg); err != nil {
					why = err.Error()
				}
			}
			switch {
			case want == "" && why != "":
				t.Errorf("%s on %s: refused (%s), the table says it runs", r.row, kind, why)
			case want != "" && !strings.Contains(why, want):
				t.Errorf("%s on %s: %q, the table says refused with %q", r.row, kind, why, want)
			}
		}
	}
	for kind, tg := range targets {
		if err := tg.HoldsBackups(); (err == nil) != (kind == KindDockerHost || kind == KindPortainer || kind == kindFlyMachines) {
			t.Errorf("volume backups on %s: %v; the table says a Docker host's and a Portainer environment's volumes are copied", kind, err)
		}
	}
	root := filepath.Join("..", "..", "..")
	guide, err := os.ReadFile(filepath.Join(root, "design", "subsystems", "deploy-targets.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(guide), "\n"+renderTargetMatrix()+"\n") {
		t.Errorf("deploy-targets.md's table differs from this test's rows; it must be exactly:\n%s", renderTargetMatrix())
	}
	owners := map[string]bool{}
	for _, r := range targetMatrix {
		for _, name := range r.pinnedBy {
			owners[name] = false
		}
	}
	_ = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for name := range owners {
			if strings.Contains(string(body), "func "+name+"(") {
				owners[name] = true
			}
		}
		return nil
	})
	for name, found := range owners {
		if !found {
			t.Errorf("the table names %s as an owning test, and there is none", name)
		}
	}
}
