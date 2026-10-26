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
	"strings"
	"testing"

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
