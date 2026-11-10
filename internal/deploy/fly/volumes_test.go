package fly

import (
	"context"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/fly/flyfake"
	"strings"
	"testing"
)

// A failed replacement must return the previous part to its recorded data.
func TestVolumeRolloutReturnsPreviousPartAfterFailedStart(t *testing.T) {
	w := newWorld(t)
	src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	var old *flyfake.Machine
	for _, m := range app.Machines {
		old = m
	}
	mounts, _ := old.Config["mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("part has no data mount: %v", old.Config)
	}
	owned, _ := w.owned.all(context.Background())
	if len(owned) != 2 || !owned[1].Data {
		t.Fatalf("data creation is not recorded: %v", owned)
	}
	failedStart := false
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaGeneration] == "2" {
			failedStart = true
			m.State = "failed"
		}
	}
	if _, err := w.release(src, 2, nil); err == nil {
		t.Fatal("failed start passed")
	}
	if !failedStart {
		t.Fatal("replacement never reached its failed start; the previous mount must be released first")
	}
	app = w.api.App(app.Name)
	if len(app.Machines) != 1 {
		t.Fatalf("previous part was not returned: %v", app.Machines)
	}
	for _, m := range app.Machines {
		if m.State != "started" || m.Metadata()[metaGeneration] != "1" {
			t.Fatalf("previous config not serving: %v", m)
		}
		got, _ := m.Config["mounts"].([]any)
		if len(got) != 1 || got[0].(map[string]any)["volume"] != mounts[0].(map[string]any)["volume"] {
			t.Fatal("data volume changed")
		}
	}
}

func TestTakeDownKeepsFlyDataWithNoRunningMachineUntilFinalBackup(t *testing.T) {
	for _, backup := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep", true: "final"}[backup], func(t *testing.T) {
			w := newWorld(t)
			catalogueNeeds(t, w)
			src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1)+"needs: {postgres: {}}\n", "a")
			if _, err := w.release(src, 1, nil); err != nil {
				t.Fatal(err)
			}
			owned, _ := w.owned.all(context.Background())
			out, err := w.driver.Teardown(context.Background(), w.p, 1, deploy.TakeDown{Owned: owned, BackedUp: backup})
			if err != nil {
				t.Fatal(err)
			}
			app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
			if backup {
				if app != nil {
					t.Fatal("final take-down retained app or volumes")
				}
				return
			}
			if app == nil || len(app.Volumes) != 2 || len(out.Left) != 1 || !out.Left[0].Data {
				t.Fatalf("data was not retained: %+v %+v", app, out)
			}
			for _, m := range app.Machines {
				if m.State != "stopped" || m.Metadata()[metaRole] != "need" {
					t.Fatalf("retained app still serves: %+v", m)
				}
			}
		})
	}
}

func TestInterruptedVolumeRolloutRecoversPreviousConfigFromCheckpoint(t *testing.T) {
	w := newWorld(t)
	src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	w.api.HoldDeletes = true
	w.api.OnCreate = func(_ string, m *flyfake.Machine) {
		if m.Metadata()[metaGeneration] == "2" {
			m.State = "failed"
		}
	}
	// The provider first refuses destroying the previous stopped part. The
	// checkpoint must already hold its config and allow recovery to start it.
	if _, err := w.release(src, 2, nil); err == nil {
		t.Fatal("interrupted release passed")
	}
	if w.noted["previous:web"] == nil {
		t.Fatal("previous config was not checkpointed before stopping")
	}
	w.api.HoldDeletes = false
	if _, err := w.driver.Resume(context.Background(), w.p, 2, w.noted, w.logf); err == nil {
		t.Fatal("interrupted generation should fail")
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	if len(app.Machines) != 1 {
		t.Fatalf("previous part not recovered: %+v", app.Machines)
	}
	for _, m := range app.Machines {
		if m.State != "started" || m.Metadata()[metaGeneration] != "1" {
			t.Fatalf("previous config not serving after recovery: %+v", m)
		}
	}
}

func TestVolumeRolloutStopsBeforeStartingOnTheRecordedVolume(t *testing.T) {
	w := newWorld(t)
	src := source(t, strings.Replace(webApp, "    public: true", "    dataDir: /data\n    public: true", 1), "a")
	if _, err := w.release(src, 1, nil); err != nil {
		t.Fatal(err)
	}
	app := w.api.App(appName(installation, deploy.HostKey(w.p.ID)))
	var oldID, vol string
	for id := range app.Machines {
		oldID = id
	}
	for id := range app.Volumes {
		vol = id
	}
	before := len(w.api.Requests())
	if _, err := w.release(src, 2, nil); err != nil {
		t.Fatal(err)
	}
	app = w.api.App(app.Name)
	if len(app.Volumes) != 1 || len(app.Machines) != 1 || app.Volumes[vol].Attached == oldID {
		t.Fatal("rollout did not reuse the recorded volume")
	}
	stop, remove, start := -1, -1, -1
	for i, r := range w.api.Requests()[before:] {
		if strings.HasSuffix(r.Path, "/"+oldID+"/stop") {
			stop = i
		}
		if r.Method == "DELETE" && strings.HasSuffix(r.Path, "/"+oldID) {
			remove = i
		}
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/machines") {
			start = i
		}
	}
	if stop < 0 || remove <= stop || start <= remove {
		t.Fatalf("not stop-destroy-start: %d %d %d", stop, remove, start)
	}
}
