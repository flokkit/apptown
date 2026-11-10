package fly

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
)

func TestVolumeCopyReleasesAttachmentAndRestartsPartAfterFailedUpload(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(map[int]string{0: "copied", 1: "failed-upload"}[code], func(t *testing.T) {
			w := newWorld(t)
			src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a")
			if _, err := w.release(src, 1, nil); err != nil {
				t.Fatal(err)
			}
			app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
			var volumeID string
			for id := range app.Volumes {
				volumeID = id
			}
			var copied bool
			w.api.OnCreate = func(_ string, m *flyfake.Machine) {
				if m.Metadata()[metaRole] != "copy" {
					return
				}
				copied = true
				mounts := m.Config["mounts"].([]any)
				if mounts[0].(map[string]any)["volume"] != volumeID {
					t.Error("copy has no data volume")
				}
				script := m.Config["init"].(map[string]any)["cmd"].([]any)[0].(string)
				if !strings.Contains(script, "tar cz -C /data .") || !strings.Contains(script, "--upload-file") {
					t.Error("volume is not exported to its object URL")
				}
				flyfake.Exit(m, code, false)
			}
			err := w.driver.CopyVolume(context.Background(), w.p, volumeID, "reg/helper@"+digestOf("h"), "https://store.example/object?signature=job")
			if (err != nil) != (code != 0) {
				t.Fatalf("upload exit ignored: %v", err)
			}
			if !copied {
				t.Fatal("copy never ran")
			}
			app = w.api.App(app.Name)
			if len(app.Machines) != 1 {
				t.Fatalf("part not recreated or job left attached: %+v", app.Machines)
			}
			for _, m := range app.Machines {
				if m.State != "started" || m.Metadata()[metaRole] != "part" {
					t.Fatal("part not running again")
				}
				if app.Volumes[volumeID].Attached != m.ID {
					t.Fatal("part lost volume")
				}
			}
		})
	}
}

func TestDeclaredCopyKeepsNeedServingAndRequiresSuccessfulExit(t *testing.T) {
	w := newWorld(t)
	catalogueNeeds(t, w)
	if _, err := w.release(source(t, webApp+"needs: {postgres: {}}\n", "a"), 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	need := needMachine(t, app)
	// No mount is sent for the live declared export.
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaRole] == "copy" {
			if _, ok := m.Config["mounts"]; ok {
				t.Error("declared export mounted database files")
			}
			flyfake.Exit(m, 1, false)
		}
	}
	if err := w.driver.DataJob(context.Background(), w.p, "postgres:16-alpine", "pg_dump -Fc --no-owner", map[string]string{"PGHOST": need.ID + ".vm." + app.Name + ".internal"}, "", "https://store.example/object", true); err == nil {
		t.Fatal("failed exporter passed")
	}
	now := needMachine(t, w.api.App(app.Name))
	if now.ID != need.ID || now.State != "started" {
		t.Fatal("declared export stopped the database")
	}
}

func TestRestoreFillsNewVolumeAndLeavesWritersStopped(t *testing.T) {
	w := newWorld(t)
	if _, err := w.release(source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a"), 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	var old string
	for id := range app.Volumes {
		old = id
	}
	resume, _, err := w.driver.StopWriters(context.Background(), w.p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var filled string
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaRole] == "copy" {
			filled = m.Config["mounts"].([]any)[0].(map[string]any)["volume"].(string)
			if filled == old {
				t.Error("restore overwrote original volume")
			}
			script := m.Config["init"].(map[string]any)["cmd"].([]any)[0].(string)
			if !strings.Contains(script, "sha256sum -c") || !strings.Contains(script, "tar xz -C /data") {
				t.Error("restore did not check then unpack the copy")
			}
		}
	}
	if err := w.driver.RestoreVolume(context.Background(), w.p, old, "reg/helper@"+digestOf("h"), "https://store.example/object", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	app = w.api.App(app.Name)
	if app.Volumes[old] == nil || app.Volumes[filled] == nil {
		t.Fatal("old volume removed before readiness")
	}
	for _, m := range app.Machines {
		if m.State != "stopped" {
			t.Fatal("a writer started during restore")
		}
	}
	if failed := resume(context.Background()); len(failed) > 0 {
		t.Fatal(failed)
	}
	app = w.api.App(app.Name)
	for _, m := range app.Machines {
		if m.State != "started" || app.Volumes[filled].Attached != m.ID {
			t.Fatal("restored volume not serving")
		}
	}
}

func TestCopyNeedsAnExplicitSuccessfulExit(t *testing.T) {
	for _, kind := range []string{"missing-code", "signal"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t)
			if _, err := w.release(source(t, webApp, "a"), 1, nil); err != nil {
				t.Fatal(err)
			}
			w.api.OnCreate = func(_ string, m *flyfake.Machine) {
				if m.Metadata()[metaRole] == "copy" {
					ex := map[string]any{}
					if kind == "signal" {
						ex["exit_code"] = 0
						ex["signal"] = 15
					}
					m.State = "stopped"
					m.Events = []map[string]any{{"type": "exit", "request": map[string]any{"exit_event": ex}}}
				}
			}
			if err := w.driver.DataJob(context.Background(), w.p, "reg/helper@"+digestOf("h"), "tar cz -C /data .", nil, "", "https://store.example/object", true); err == nil {
				t.Fatal("a missing or signalled exit passed as a completed copy")
			}
		})
	}
}

func TestUnreachableStoppedWriterIsNotConfirmedStopped(t *testing.T) {
	w := newWorld(t)
	if _, err := w.release(source(t, webApp, "a"), 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	for _, m := range app.Machines {
		w.api.Set(app.Name, m.ID, "stopped", "unreachable", "")
	}
	w.driver.options.machineWait = 10 * time.Millisecond
	if _, _, err := w.driver.StopWriters(context.Background(), w.p, nil, nil); err == nil {
		t.Fatal("an unreachable writer was confirmed stopped")
	}
}

func TestReleaseAfterStoppedRestoreRetiresOldVolumeOnlyWhenReady(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "failed"}[fail], func(t *testing.T) {
			w := newWorld(t)
			src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a")
			if _, err := w.release(src, 1, nil); err != nil {
				t.Fatal(err)
			}
			app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
			var old string
			for id := range app.Volumes {
				old = id
			}
			if _, _, err := w.driver.StopWriters(context.Background(), w.p, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := w.driver.RestoreVolume(context.Background(), w.p, old, "reg/helper@"+digestOf("h"), "https://store.example/object", strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			if fail {
				w.api.OnCreate = func(_ string, m *flyfake.Machine) {
					if m.Metadata()[metaGeneration] == "2" {
						m.State = "failed"
					}
				}
			}
			_, err := w.release(src, 2, nil)
			app = w.api.App(app.Name)
			if fail {
				if err == nil || app.Volumes[old] == nil {
					t.Fatal("failed replacement discarded the previous volume")
				}
				for _, m := range app.Machines {
					if m.State != "stopped" {
						t.Fatal("failed release started a writer that the restore left stopped")
					}
				}
			} else if err != nil || app.Volumes[old] != nil || len(app.Volumes) != 1 {
				t.Fatalf("ready release did not retire the old restore volume: %v %+v", err, app.Volumes)
			} else if live, err := w.driver.VolumeIDs(context.Background(), w.p); err != nil || live[old] || len(live) != 1 {
				// Fly keeps listing a deleted volume in a destroy state: it is not held data.
				t.Fatalf("a retired volume still reads as held data: %v %v", live, err)
			}
		})
	}
}

// Fly reports signal -1 for a Machine that ended on its own; only a real
// signal (or a non-zero code, or out of memory) is a failed one-off.
func TestOneOffExitFailsOnlyOnARealSignal(t *testing.T) {
	for _, c := range []struct {
		event  map[string]any
		failed bool
	}{
		{map[string]any{"exit_code": 0.0, "signal": -1.0}, false},
		{map[string]any{"exit_code": 0.0}, false},
		{map[string]any{"exit_code": 0.0, "signal": 0.0}, false},
		{map[string]any{"exit_code": 0.0, "signal": 15.0}, true},
		{map[string]any{"exit_code": 1.0, "signal": -1.0}, true},
		{map[string]any{"exit_code": 0.0, "signal": -1.0, "oom_killed": true}, true},
	} {
		ex, ok := exitOf([]event{{Request: map[string]any{"exit_event": c.event}}})
		if !ok || ex.failed() != c.failed {
			t.Errorf("exit %v: failed %t, want %t", c.event, ex.failed(), c.failed)
		}
	}
}
