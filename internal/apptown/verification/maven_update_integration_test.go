//go:build integration

package verification

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/stock"
	"golang.org/x/mod/semver"
)

// The java pack's update fix, read through the pack's own guidance list, runs
// unchanged where the janitor runs it: the listing through ListUpdates on the
// agent image, each command after `mise install` in the part's folder as the
// fix step runs it. Maven Central is the real registry, so the assertions hold
// for any release it publishes later: moves stay inside their bound, the
// targeted fix moves only its targets, and nothing touches the Java release,
// the toolchain or the Dockerfile.
func TestIntegrationShippedMavenUpdateFixRunsInTheAgentImage(t *testing.T) {
	d := javaPackUpdateFix(t)
	db := testDB(t)
	executor := Executor{Run: LocalChecks(execution.Runner{}, nil), Local: true, AgentImage: shotsAgentImage()}
	toolchain, err := stock.FS.ReadFile("toolchains/java.toml")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".apptown/app.yaml": "version: 2\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    tools: [java]\n",
		"mise.toml":         string(toolchain),
		"Dockerfile":        "FROM eclipse-temurin:21-jre\nCOPY target/app.jar /app.jar\n",
		"pom.xml": `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.3.0</version>
    <relativePath/>
  </parent>
  <groupId>app</groupId>
  <artifactId>app</artifactId>
  <version>0.0.1-SNAPSHOT</version>
  <properties>
    <java.version>21</java.version>
    <commons-io.version>2.11.0</commons-io.version>
  </properties>
  <dependencies>
    <dependency><groupId>com.fasterxml.jackson.core</groupId><artifactId>jackson-annotations</artifactId></dependency>
    <dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId><version>2.0.9</version></dependency>
    <dependency><groupId>commons-io</groupId><artifactId>commons-io</artifactId><version>${commons-io.version}</version></dependency>
    <dependency><groupId>org.apache.commons</groupId><artifactId>commons-lang3</artifactId><version>3.12.0</version></dependency>
  </dependencies>
</project>
`,
	}
	parent := regexp.MustCompile(`(?s)<parent>.*?<version>([^<]+)</version>`)
	for _, tc := range []struct {
		name, bound, command string
		moved                []string
		parentMajor          string // empty: the parent stays at 3.3.0
	}{
		{"routine", "routine", d.Run, []string{"com.fasterxml.jackson.core:jackson-annotations", "commons-io:commons-io", "org.apache.commons:commons-lang3", "org.slf4j:slf4j-api"}, "v3"},
		{"aggressive", "aggressive", d.RunMajor, []string{"com.fasterxml.jackson.core:jackson-annotations", "commons-io:commons-io", "org.apache.commons:commons-lang3", "org.slf4j:slf4j-api"}, "major"},
		{"targeted", "routine", "", []string{"commons-io:commons-io", "org.slf4j:slf4j-api"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case starts from its own tree; most of the time is Maven
			// downloading, so the three run at once.
			t.Parallel()
			tree := t.TempDir()
			for name, body := range files {
				path := filepath.Join(tree, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			command := tc.command
			if tc.name == "targeted" {
				// Two targets, as a scan with two findings asks: the
				// substitution quotes each, the command joins them.
				targeted, err := fix.UpdateCommand(d, []string{"org.slf4j:slf4j-api", "commons-io:commons-io"}, true)
				if err != nil {
					t.Fatal(err)
				}
				command = targeted
			}
			before, err := ListUpdates(t.Context(), db, executor, tree, []fix.Declaration{d})
			if err != nil || len(before) != 1 {
				t.Fatalf("listing before: %+v %v", before, err)
			}
			bound, err := integration.RegistryBindings(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			amb := agentenv.AmbientFor(bound, tree)
			out := executor.Run(t.Context(), execution.Request{Workspace: tree, Image: executor.AgentImage, Argv: []string{"sh", "-c", trust.Run + `; export PATH="$MISE_DATA_DIR/shims:$PATH"; mise install -y -q && ` + command}, Env: amb.Env, Files: amb.Files, Timeout: 10 * time.Minute, Name: execution.UniqueName("apptown-maven-update-test"), Limits: execution.DefaultTaskLimits})
			if !out.Exited0() || out.RunErr != nil {
				t.Fatalf("update command: exit %d %v\n%s", out.ExitCode, out.RunErr, out.Output)
			}
			after, err := ListUpdates(t.Context(), db, executor, tree, []fix.Declaration{d})
			if err != nil || len(after) != 1 {
				t.Fatalf("listing after: %+v %v", after, err)
			}
			moves, err := fix.CompareUpdate(before[0].Packages, after[0].Packages, tc.bound, nil)
			if err != nil {
				t.Fatalf("the %s update was not a %s move: %v", tc.name, tc.bound, err)
			}
			var names []string
			for _, m := range moves {
				names = append(names, m.Package)
				if strings.ContainsAny(m.To, "+-") {
					t.Errorf("%s moved to a qualified version %s", m.Package, m.To)
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.moved) {
				t.Errorf("moved %v, want %v (%+v)", names, tc.moved, moves)
			}
			pom, err := os.ReadFile(filepath.Join(tree, "pom.xml"))
			if err != nil {
				t.Fatal(err)
			}
			got := parent.FindSubmatch(pom)
			if got == nil {
				t.Fatalf("no parent version in\n%s", pom)
			}
			version := "v" + string(got[1])
			switch tc.parentMajor {
			case "":
				if version != "v3.3.0" {
					t.Errorf("the targeted fix moved the parent to %s", version)
				}
			case "major":
				if semver.Compare(semver.Major(version), "v3") <= 0 {
					t.Errorf("the aggressive update kept the parent at %s", version)
				}
			default:
				if semver.Major(version) != tc.parentMajor || version == "v3.3.0" {
					t.Errorf("the routine update moved the parent to %s", version)
				}
			}
			if !strings.Contains(string(pom), "<java.version>21</java.version>") {
				t.Errorf("the Java release changed:\n%s", pom)
			}
			for _, name := range []string{"mise.toml", "Dockerfile"} {
				raw, err := os.ReadFile(filepath.Join(tree, name))
				if err != nil || string(raw) != files[name] {
					t.Errorf("%s changed: %q %v", name, raw, err)
				}
			}
		})
	}
}

func javaPackUpdateFix(t *testing.T) fix.Declaration {
	t.Helper()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	blocks, skipped := wow.ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	var ds []fix.Declaration
	for _, p := range packs {
		for _, b := range blocks {
			if p.Name == "java" && slices.Contains(p.Guidance, b.Name) {
				declared, err := fix.Parse(b.FixesJSON)
				if err != nil {
					t.Fatal(err)
				}
				ds = append(ds, declared...)
			}
		}
	}
	if len(ds) != 1 || ds[0].Ecosystem != "Maven" {
		t.Fatalf("the java pack adopts %+v", ds)
	}
	return ds[0]
}
