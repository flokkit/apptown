package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func observedFixture(t *testing.T) (Registration, map[string]any, string) {
	t.Helper()
	r, env := environmentFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.Adapter = "docker"
	r.ReleaseDir = ""
	r.ExecutableSHA256 = ""
	r.Container = strings.Repeat("b", 64)
	r.Image = "sha256:" + strings.Repeat("c", 64)
	r.DockerEngine = "owned-engine"
	r.DockerSocket = "/run/owned-docker.sock"
	r.Supervisor = filepath.Join(root, "apptown-update")
	if err := os.WriteFile(r.Supervisor, []byte("stable fixture artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	r.SupervisorSHA256, err = hashFile(r.Supervisor)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := ManagedUnit(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "managed.service")
	if err := os.WriteFile(path, unit, 0600); err != nil {
		t.Fatal(err)
	}
	r.UnitSHA256, err = hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	facts := "MainPID=0\nActiveState=inactive\nSubState=dead\nFragmentPath=" + path + "\nDropInPaths=\nNeedDaemonReload=no\nLoadState=loaded\n"
	row := map[string]any{"Id": r.Container, "Image": r.Image, "Config": map[string]any{"Env": env}, "State": map[string]any{"Running": false, "Pid": 0, "Status": "exited"}, "HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": "no"}, "NetworkMode": "host"}, "Mounts": []map[string]any{{"Type": "bind", "Source": r.DataDir, "Destination": r.DataDir}, {"Type": "bind", "Source": r.StateDir, "Destination": r.StateDir}}}
	return r, row, facts
}
func fixtureHost(r Registration, row map[string]any, facts string) Host {
	return Host{Run: func(_ context.Context, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "systemctl" {
			return []byte(facts), nil
		}
		if len(args) < 4 || args[0] != "docker" || args[1] != "--host" || args[2] != "unix://"+r.DockerSocket {
			return nil, errors.New("wrong registered Docker scope")
		}
		switch args[3] {
		case "info":
			return []byte(r.DockerEngine), nil
		case "inspect":
			if len(args) != 5 || args[4] != r.Container {
				return nil, errors.New("wrong registered container")
			}
			return json.Marshal([]any{row})
		default:
			return nil, errors.New("observation tried a mutation")
		}
	}}
}
func TestDockerObservationRefusesChangedRegisteredFacts(t *testing.T) {
	cases := map[string]func(map[string]any){
		"container": func(r map[string]any) { r["Id"] = strings.Repeat("e", 64) },
		"image":     func(r map[string]any) { r["Image"] = "sha256:" + strings.Repeat("e", 64) },
		"restart": func(r map[string]any) {
			r["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "always"}
		},
		"network":         func(r map[string]any) { r["HostConfig"].(map[string]any)["NetworkMode"] = "bridge" },
		"data source":     func(r map[string]any) { r["Mounts"].([]map[string]any)[0]["Source"] = "/other" },
		"missing control": func(r map[string]any) { r["Mounts"] = r["Mounts"].([]map[string]any)[:1] },
		"environment":     func(r map[string]any) { r["Config"] = map[string]any{"Env": []string{"APPTOWN_DATA=/other"}} },
		"unsettled stopped": func(r map[string]any) {
			r["State"] = map[string]any{"Running": false, "Pid": 0, "Status": "restarting"}
		},
	}
	r, row, facts := observedFixture(t)
	got, err := fixtureHost(r, row, facts).Observe(context.Background(), r)
	if err != nil || got.Running {
		t.Fatalf("confirmed stopped control refused: %+v %v", got, err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, row, facts := observedFixture(t)
			change(row)
			if _, err := fixtureHost(r, row, facts).Observe(context.Background(), r); err == nil {
				t.Fatal("accepted changed registered fact")
			}
		})
	}
}
func TestUnloadedOrStaleSystemdConfigurationIsUnavailable(t *testing.T) {
	r, row, facts := observedFixture(t)
	for _, bad := range []string{strings.ReplaceAll(facts, "NeedDaemonReload=no", "NeedDaemonReload=yes"), strings.ReplaceAll(facts, "LoadState=loaded", "LoadState=not-found"), strings.ReplaceAll(facts, "DropInPaths=\n", "DropInPaths=/other.conf\n")} {
		if _, err := fixtureHost(r, row, bad).Observe(context.Background(), r); err == nil {
			t.Fatal("accepted stale or altered loaded unit")
		}
	}
	unavailable := Host{Run: func(context.Context, []string) ([]byte, error) { return nil, errors.New("service manager unavailable") }}
	if _, err := unavailable.Observe(context.Background(), r); err == nil {
		t.Fatal("unavailable service manager became stopped")
	}
}
