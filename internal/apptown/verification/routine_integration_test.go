//go:build integration

package verification

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/oci"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/routinechange"
)

// This fixture seeds npm's real content-addressed cache with exact test-owned
// tarballs. The unchanged production observer then runs without a network. No
// registry, project command or host credential participates in this proof.
func TestIntegrationRoutineNPMObservesInstalledScriptsWithoutExecutingThem(t *testing.T) {
	tarball := func(version string, script bool) []byte {
		t.Helper()
		manifest := map[string]any{"name": "example", "version": version}
		if script {
			manifest["scripts"] = map[string]string{"postinstall": "touch /work/script-executed"}
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		archive := tar.NewWriter(gz)
		if err = archive.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err = archive.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err = archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err = gz.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	snapshot := func(version string, tarball []byte) routinechange.NPMSnapshot {
		s := routineFixture(version)
		sum := sha512.Sum512(tarball)
		s.Lock = []byte(strings.ReplaceAll(string(s.Lock), base64.StdEncoding.EncodeToString(make([]byte, 64)), base64.StdEncoding.EncodeToString(sum[:])))
		return s
	}
	for _, hiddenScript := range []bool{false, true} {
		t.Run(fmt.Sprint(hiddenScript), func(t *testing.T) {
			oldTar, newTar := tarball("1.0.0", false), tarball("1.0.1", hiddenScript)
			before, after := snapshot("1.0.0", oldTar), snapshot("1.0.1", newTar)
			candidate, err := routinechange.CompareNPM(before, after, "patch")
			if err != nil {
				t.Fatalf("fixture must reach independent observation: %v", err)
			}
			runner := execution.Runner{}
			executor := Executor{AgentImage: shotsAgentImage(), Local: true, Image: runner.PrepareImage, Run: func(ctx context.Context, req execution.Request) execution.Outcome {
				req.Network = "none"
				const seed = `const fs=require('node:fs'),cp=require('node:child_process');
fs.writeFileSync('/tmp/seed-user.npmrc','');fs.writeFileSync('/tmp/seed-global.npmrc','');
for(const side of ['before','after']){const out=cp.spawnSync('npm',['cache','add','/fixture-'+side+'.tgz','--ignore-scripts','--offline','--audit=false','--fund=false','--cache=/tmp/npm-'+side,'--userconfig=/tmp/seed-user.npmrc','--globalconfig=/tmp/seed-global.npmrc'],{env:{PATH:'/usr/local/bin:/usr/bin:/bin',HOME:'/tmp'},encoding:'utf8'});if(out.error||out.status!==0)throw new Error(out.error||out.stderr);}
require('/routine-observer.cjs');
if(fs.existsSync('/work/script-executed'))throw new Error('dependency lifecycle script executed');`
				req.Files = append(req.Files, execution.File{Path: "/fixture-before.tgz", Content: oldTar, Mode: 0o444}, execution.File{Path: "/fixture-after.tgz", Content: newTar, Mode: 0o444}, execution.File{Path: "/seed-cache.cjs", Content: []byte(seed), Mode: 0o444})
				req.Argv = []string{"node", "/seed-cache.cjs"}
				out := runner.Run(ctx, execution.Docker, req)
				if !out.Exited0() || out.RunErr != nil {
					t.Fatalf("real frozen npm installation failed: termination=%s exit=%d error=%v cleanup=%v\n%s", out.Termination, out.ExitCode, out.RunErr, out.CleanupErr, string(out.Output))
				}
				return out
			}}
			evidence, err := validateNPM(t.Context(), executor, t.TempDir(), before, after, candidate)
			if hiddenScript {
				if err == nil || !strings.Contains(err.Error(), "lifecycle") {
					t.Fatalf("hidden installed script accepted: %+v %v", evidence, err)
				}
			} else if err != nil || evidence.Image == "" || evidence.NPM == "" {
				t.Fatalf("real frozen install not observed: %+v %v", evidence, err)
			}
		})
	}
}

// The registry boundary is covered separately by real in-process manifests.
// Here two test-owned images exercise the unchanged runtime observer on Docker;
// only immutable-ID fixture selection replaces external registry preparation.
func TestIntegrationRoutineImageObservesActualVersionWithoutEntrypoint(t *testing.T) {
	runner := execution.Runner{}
	dk := docker.Scope{Client: docker.CLI{}}
	base := runner.PrepareImage(t.Context(), execution.ImageRequest{Tag: shotsAgentImage()})
	if base.Err != "" || base.ID == "" {
		t.Fatalf("maintained agent image: %+v", base)
	}
	raw, err := dk.Run(t.Context(), "image", "inspect", "--format", "{{json .Config.Env}} {{.Os}} {{.Architecture}}", base.ID)
	if err != nil {
		t.Fatalf("observing fixture platform/version declaration: %s %v", raw, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var env []string
	if err = dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	rest := strings.Fields(string(raw[dec.InputOffset():]))
	if len(rest) != 2 {
		t.Fatalf("fixture platform: %s", raw)
	}
	platform, err := oci.Platform(rest[0], rest[1])
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "NODE_VERSION=") {
			version = strings.TrimPrefix(entry, "NODE_VERSION=")
		}
	}
	if version == "" {
		t.Fatal("maintained image has no Node version declaration")
	}
	data := t.TempDir()
	prefix := "apptown-routine-image-" + strings.ToLower(filepath.Base(filepath.Dir(data))+"-"+filepath.Base(data))
	removeOwned := func(tag string) {
		t.Helper()
		t.Cleanup(func() {
			ctx := context.Background()
			exists, e := dk.Exists(ctx, "image", "inspect", tag)
			if e != nil {
				t.Errorf("observing owned fixture image %s: %v", tag, e)
				return
			}
			if exists {
				if out, e := dk.Run(ctx, "image", "rm", tag); e != nil {
					t.Errorf("removing owned fixture image %s: %s %v", tag, out, e)
				}
			}
		})
	}
	// BuildKit interprets a bare sha256 image ID in FROM as a registry name.
	// Give the already observed immutable image a unique, test-owned local tag.
	baseTag := prefix + ":base"
	removeOwned(baseTag)
	if out, e := dk.Run(t.Context(), "image", "tag", base.ID, baseTag); e != nil {
		t.Fatalf("pinning fixture base: %s %v", out, e)
	}
	ids := map[string]string{}
	for i, letter := range []string{"a", "b"} {
		dir := filepath.Join(data, letter)
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		routineWrite(t, filepath.Join(dir, "marker"), []byte(letter))
		routineWrite(t, filepath.Join(dir, "Dockerfile"), []byte("FROM "+baseTag+"\nCOPY marker /routine-marker\nENTRYPOINT [\"sh\",\"-c\",\"touch /work/unexpected-entrypoint\"]\n"))
		tag := prefix + ":" + letter
		removeOwned(tag)
		image := runner.PrepareImage(t.Context(), execution.ImageRequest{Tag: tag, Tree: dir, Context: ".", AllowBuild: true})
		if image.Err != "" || image.ID == "" {
			t.Fatalf("fixture build %d: %+v", i, image)
		}
		ids["sha256:"+strings.Repeat(letter, 64)] = image.ID
	}
	if pinned := runner.PrepareImage(t.Context(), execution.ImageRequest{Tag: baseTag}); pinned.Err != "" || pinned.ID != base.ID {
		t.Fatalf("fixture base moved during builds: %+v expected %s", pinned, base.ID)
	}
	candidate := routinechange.ImageCandidate{Before: "docker.io/library/node:" + version + "-fixture@sha256:" + strings.Repeat("a", 64), After: "docker.io/library/node:" + version + "-fixture@sha256:" + strings.Repeat("b", 64), Version: version, Platform: platform}
	inspector := routineInspector{dataDir: data, imageReader: func(_ context.Context, s oci.Source) (oci.Observation, error) {
		_, digest, _ := strings.Cut(s.Reference, "@")
		return oci.Observation{Repository: "index.docker.io/library/node", Manifest: digest, ConfigDigest: ids[digest], Platform: platform, Config: []byte(`{"fixture":"same-runtime-configuration"}`)}, nil
	}}
	runs := 0
	inspector.executor = Executor{Local: true, Image: func(_ context.Context, req execution.ImageRequest) execution.ImageAnswer {
		if !req.Anonymous || req.Registry != nil {
			t.Fatal("fixture preparation was not anonymous")
		}
		_, digest, _ := strings.Cut(req.Pull, "@")
		return execution.ImageAnswer{ID: ids[digest]}
	}, Run: func(ctx context.Context, req execution.Request) execution.Outcome {
		runs++
		out := runner.Run(ctx, execution.Docker, req)
		if _, err := os.Stat(filepath.Join(req.Workspace, "unexpected-entrypoint")); !os.IsNotExist(err) {
			t.Fatal("image entrypoint executed")
		}
		return out
	}}
	got, err := inspector.validateImage(t.Context(), candidate)
	if err != nil || len(got) != 2 || runs != 2 || got[0].Node != version || got[1].Node != version {
		t.Fatalf("actual image observations: %+v %v runs=%d", got, err, runs)
	}
	candidate.Version = "0.0.1"
	if _, err = inspector.validateImage(t.Context(), candidate); err == nil {
		t.Fatal("actual runtime accepted a false declared version")
	}
}
