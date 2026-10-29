package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/stock"
)

// A static site's image is built from a Dockerfile of its own that names
// the command serving it on its port (docker.StaticSiteDockerfile): the
// image describes itself, so a target runs it without adding a command.
// The same folder served on two ports is two images, each with its port.
func TestAReleaseBuildGivesEachStaticImageItsOwnServingCommand(t *testing.T) {
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n  mirror:\n    static: site\n    ports: [\"9090\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	script, files, images, err := releaseBuildRequest(tree, deployment.PlanRelease(c), "linux/amd64", stock.Images().StaticServer)
	if err != nil || len(images) != 2 || len(files) != 2 {
		t.Fatalf("request: %d images, %d files, %v", len(images), len(files), err)
	}
	want := map[string]string{
		"/forge/release/part-web.Dockerfile":    docker.StaticSiteDockerfile(stock.Images().StaticServer, 8080),
		"/forge/release/part-mirror.Dockerfile": docker.StaticSiteDockerfile(stock.Images().StaticServer, 9090),
	}
	for _, f := range files {
		if want[f.Path] != string(f.Content) || !strings.Contains(script, "'-f' '"+f.Path+"'") {
			t.Fatalf("%s:\n%s\nscript:\n%s", f.Path, f.Content, script)
		}
		if !strings.Contains(string(f.Content), "\nCMD [\"sh\",\"-c\",") {
			t.Fatalf("%s has no command of its own:\n%s", f.Path, f.Content)
		}
	}
}

// A static site with a declared build is exported inside the release task,
// on the task's own builder (no --builder, no --platform: the task's
// BUILDX_BUILDER and the builder's own platform), checked, and its output
// folder packed for the release's platform with the file server's
// Dockerfile. The controller receives only the packed image.
func TestAReleaseBuildExportsChecksAndPacksADeclaredStaticSite(t *testing.T) {
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  site:\n    public: true\n    static: { build: web, target: out, output: app/dist }\n"))
	if err != nil {
		t.Fatal(err)
	}
	script, files, images, err := releaseBuildRequest(tree, deployment.PlanRelease(c), "linux/arm64", stock.Images().StaticServer)
	if err != nil || len(images) != 1 || len(files) != 1 || images[0].Name != "part-site" {
		t.Fatalf("request: %+v %+v %v", images, files, err)
	}
	export := "/apptown/state/static/part-site"
	lines := strings.Split(script, "\n")
	at := func(prefix string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		t.Fatalf("no line %q in:\n%s", prefix, script)
		return -1
	}
	// Each build is given the extra certificates as its secret, from the
	// task's own variable (internal/trust).
	secret := " ${APPTOWN_TRUST:+--secret id=apptown-trust,env=APPTOWN_TRUST --build-arg APPTOWN_TRUST_SUM} "
	exportLine := at("'docker' 'buildx' 'build' '--provenance=false' '--sbom=false' '--target' 'out' '--output' 'type=local,dest=" + export + "'" + secret + "'/work/web'")
	check := at("[ ! -L '" + export + "/app' ]")
	pack := at("'docker' 'buildx' 'build' '--platform' 'linux/arm64'")
	if !(exportLine < check && check < pack) || !strings.HasSuffix(lines[pack], "'-f' '/forge/release/part-site.Dockerfile'"+secret+"'"+export+"/app/dist'") {
		t.Fatalf("export, check, then pack:\n%s", script)
	}
	if strings.Contains(lines[exportLine], "--platform") || strings.Contains(script, "--builder") {
		t.Fatalf("the export names a platform or a builder:\n%s", script)
	}
	if string(files[0].Content) != docker.StaticSiteDockerfile(stock.Images().StaticServer, 80) {
		t.Fatalf("the pack's Dockerfile:\n%s", files[0].Content)
	}
}
