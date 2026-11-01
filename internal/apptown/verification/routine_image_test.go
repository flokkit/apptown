package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/oci"
)

func imageRoutineRepository(t *testing.T, contracts ...string) (string, string, workflow.Source) {
	t.Helper()
	data := t.TempDir()
	source := workflow.Source{ProjectID: idstest.N(11), ChangeID: idstest.N(12), RunID: idstest.N(13), RootRunID: idstest.N(13), Branch: "change", DefaultBranch: "main", Slug: "image-app"}
	repo, err := source.Repository(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repo, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	routineGit(t, repo, "init", "-b", "main")
	contract := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"
	if len(contracts) > 0 {
		contract = contracts[0]
	}
	routineWrite(t, filepath.Join(repo, ".apptown/app.yaml"), []byte(contract))
	for _, digest := range []string{"a", "b"} {
		routineWrite(t, filepath.Join(repo, "Dockerfile"), []byte("FROM --platform=linux/amd64 docker.io/library/node:22.14.0-bookworm-slim@sha256:"+strings.Repeat(digest, 64)+"\nUSER node\nCMD [\"node\",\"app.js\"]\n"))
		routineGit(t, repo, "add", ".")
		routineGit(t, repo, "commit", "-m", digest)
		if digest == "a" {
			source.ChangeBaseSHA = routineGit(t, repo, "rev-parse", "HEAD")
			routineGit(t, repo, "checkout", "-b", "change")
		}
	}
	return data, repo, source
}
func imageRoutineInspector(t *testing.T, data string) (routineInspector, *int) {
	t.Helper()
	calls := new(int)
	inspector := NewRoutineInspector(data, &scm.RepositoryLocks{}, Executor{})
	inspector.imageReader = func(_ context.Context, s oci.Source) (oci.Observation, error) {
		*calls++
		if s.Credential != (oci.Credential{}) {
			t.Fatal("image metadata received credentials")
		}
		_, digest, _ := strings.Cut(s.Reference, "@")
		return oci.Observation{Repository: "index.docker.io/library/node", Manifest: digest, ConfigDigest: "sha256:" + strings.Repeat("c", 64), Platform: "linux/amd64", Config: json.RawMessage(`{"config":{"User":"node"}}`)}, nil
	}
	inspector.executor = Executor{Local: true, Image: func(_ context.Context, r execution.ImageRequest) execution.ImageAnswer {
		*calls++
		if !r.Anonymous || r.Registry != nil || r.AllowBuild || r.Tag != r.Pull || !strings.Contains(r.Pull, "@sha256:") {
			t.Fatalf("unconfined image preparation: %+v", r)
		}
		return execution.ImageAnswer{ID: "sha256:" + strings.Repeat("c", 64)}
	}, Run: func(_ context.Context, r execution.Request) execution.Outcome {
		*calls++
		if r.Network != "none" || !r.NoEntrypoint || r.Image != "sha256:"+strings.Repeat("c", 64) || len(r.Env) != 0 || len(r.Files) != 1 || string(r.Files[0].Content) != string(nodeVersionObserver) {
			t.Fatalf("unconfined runtime observer: %+v", r)
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: []byte(`{"node":"22.14.0","os":"linux","arch":"x64","observer":"node-version-v1"}`)}
	}}
	return inspector, calls
}
func TestRoutineImageInspectionNeedsExactRuntimeAndCurrentSource(t *testing.T) {
	data, repo, source := imageRoutineRepository(t)
	inspector, calls := imageRoutineInspector(t, data)
	result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{BaseImages: true})
	if err != nil || result.Classification != "routine-image" || len(result.Images) != 2 || *calls != 6 {
		t.Fatalf("exact image-only classification: %+v %v calls=%d", result, err, *calls)
	}
	*calls = 0
	result, err = inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{Dependencies: "patch"})
	if err != nil || result.Classification != "requires-person" || *calls != 0 {
		t.Fatalf("image ran without consent: %+v %v calls=%d", result, err, *calls)
	}
	baseRun := inspector.executor.Run
	moved := false
	inspector.executor.Run = func(ctx context.Context, r execution.Request) execution.Outcome {
		if !moved {
			moved = true
			routineWrite(t, filepath.Join(repo, "extra.txt"), []byte("new source"))
			routineGit(t, repo, "add", ".")
			routineGit(t, repo, "commit", "-m", "moved")
		}
		return baseRun(ctx, r)
	}
	result, err = inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{BaseImages: true})
	if err != nil || result.Classification != "cannot-assess" || !strings.Contains(result.Reason, "source moved") {
		t.Fatalf("stale image evidence: %+v %v", result, err)
	}
}
func TestRoutineImageInspectionRefusesMismatchedIdentityAndVersion(t *testing.T) {
	for _, test := range []string{"prepared image", "runtime version", "runtime configuration", "platform", "manifest", "cleanup"} {
		t.Run(test, func(t *testing.T) {
			data, _, source := imageRoutineRepository(t)
			inspector, _ := imageRoutineInspector(t, data)
			reader := inspector.imageReader
			reads := 0
			inspector.imageReader = func(ctx context.Context, s oci.Source) (oci.Observation, error) {
				v, e := reader(ctx, s)
				reads++
				switch test {
				case "runtime configuration":
					if reads == 2 {
						v.Config = []byte(`{"config":{"User":"root"}}`)
					}
				case "platform":
					v.Platform = "linux/arm64"
				case "manifest":
					v.Manifest = "sha256:" + strings.Repeat("d", 64)
				}
				return v, e
			}
			if test == "prepared image" {
				inspector.executor.Image = func(context.Context, execution.ImageRequest) execution.ImageAnswer {
					return execution.ImageAnswer{ID: "sha256:" + strings.Repeat("d", 64)}
				}
				inspector.executor.Run = func(context.Context, execution.Request) execution.Outcome {
					t.Fatal("wrong image ran")
					return execution.Outcome{}
				}
			}
			run := inspector.executor.Run
			if test == "runtime version" || test == "cleanup" {
				inspector.executor.Run = func(ctx context.Context, r execution.Request) execution.Outcome {
					v := run(ctx, r)
					if test == "runtime version" {
						v.Report = []byte(`{"node":"23.0.0","os":"linux","arch":"x64","observer":"node-version-v1"}`)
					} else {
						v.CleanupErr = os.ErrPermission
					}
					return v
				}
			}
			result, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{BaseImages: true})
			if err != nil || result.Classification != "cannot-assess" {
				t.Fatalf("mismatched image accepted: %+v %v", result, err)
			}
		})
	}
}

func TestRoutineImageInspectionRefusesStorageAndUnexplainedFilesBeforeEffects(t *testing.T) {
	for _, contract := range []string{
		"app:\n  web: {build: ., ports: [8080], public: true, dataDir: /data}\n",
		"app:\n  web: {build: ., ports: [8080], public: true}\nneeds: {postgres: {}}\n",
	} {
		parsed, parseErr := appcontract.Parse([]byte(contract))
		if parseErr != nil || (len(parsed.Needs) == 0 && parsed.Parts[0].DataDir == "") {
			t.Fatalf("storage control must contain a valid storage declaration: %+v %v", parsed, parseErr)
		}
		data, _, source := imageRoutineRepository(t, contract)
		inspector, calls := imageRoutineInspector(t, data)
		got, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{BaseImages: true})
		if err != nil || got.Classification != "requires-person" || *calls != 0 {
			t.Fatalf("storage image admitted: %+v %v calls=%d", got, err, *calls)
		}
	}
	data, repo, source := imageRoutineRepository(t)
	inspector, calls := imageRoutineInspector(t, data)
	routineWrite(t, filepath.Join(repo, "app.js"), []byte("changed application code"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "mixed source")
	got, err := inspector.InspectRoutine(t.Context(), source, workflow.StandingCategories{BaseImages: true})
	if err != nil || got.Classification != "requires-person" || *calls != 0 || len(got.ChangedFiles) != 2 {
		t.Fatalf("unexplained source executed: %+v %v calls=%d", got, err, *calls)
	}
}

func TestRoutineMixedUpdatesRequireBothCategoriesAndBothWitnesses(t *testing.T) {
	data, repo, source := imageRoutineRepository(t)
	routineGit(t, repo, "checkout", "main")
	before := routineFixture("1.0.0")
	routineWrite(t, filepath.Join(repo, "package.json"), before.Manifest)
	routineWrite(t, filepath.Join(repo, "package-lock.json"), before.Lock)
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "original dependencies")
	source.ChangeBaseSHA = routineGit(t, repo, "rev-parse", "HEAD")
	routineGit(t, repo, "checkout", "change")
	routineGit(t, repo, "rebase", "main")
	after := routineFixture("1.0.1")
	routineWrite(t, filepath.Join(repo, "package.json"), after.Manifest)
	routineWrite(t, filepath.Join(repo, "package-lock.json"), after.Lock)
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "dependency update")
	for _, categories := range []workflow.StandingCategories{{BaseImages: true}, {Dependencies: "patch"}, {Dependencies: "patch", BaseImages: true}} {
		inspector, calls := imageRoutineInspector(t, data)
		imageExec := inspector.executor
		npmExec := routineExecutor(t, nil)
		npmCalls := 0
		inspector.executor.AgentImage = "test-agent"
		inspector.executor.Image = func(ctx context.Context, r execution.ImageRequest) execution.ImageAnswer {
			if r.Anonymous {
				return imageExec.Image(ctx, r)
			}
			npmCalls++
			return npmExec.Image(ctx, r)
		}
		inspector.executor.Run = func(ctx context.Context, r execution.Request) execution.Outcome {
			if r.Files[0].Path == "/routine-image.cjs" {
				return imageExec.Run(ctx, r)
			}
			npmCalls++
			return npmExec.Run(ctx, r)
		}
		got, err := inspector.InspectRoutine(t.Context(), source, categories)
		if err != nil {
			t.Fatal(err)
		}
		if !categories.BaseImages || categories.Dependencies == "" {
			if got.Classification != "requires-person" || *calls != 0 || npmCalls != 0 {
				t.Fatalf("mixed update partially executed: %+v image=%d npm=%d", got, *calls, npmCalls)
			}
		} else if got.Classification != "routine-dependency-and-image" || got.Validation == nil || len(got.Images) != 2 || len(got.ChangedFiles) != 3 || *calls != 6 || npmCalls != 2 {
			t.Fatalf("missing combined witnesses: %+v image=%d npm=%d", got, *calls, npmCalls)
		}
	}
}
