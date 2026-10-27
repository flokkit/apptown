//go:build integration && linux

package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	polling "github.com/flokkit/apptown/internal/poll"
)

// This owns one disposable systemd host. The nested Docker daemon has no host
// socket/network and shares PID visibility only with its own host controller.
func TestIntegrationManagedSupervisorAdaptersSurviveRestartAndFenceBoot(t *testing.T) {
	for _, adapter := range []string{"systemd", "docker"} {
		if !t.Run(adapter, func(t *testing.T) { testManagedAdapter(t, adapter) }) {
			return // Includes cleanup/kernel observation failure: do not boot another fixture.
		}
	}
}
func testManagedAdapter(t *testing.T, adapter string) {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := os.MkdirTemp("", "u2-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(scratch); err != nil {
			t.Error(err)
		}
	})
	for _, dir := range []string{"state", "data", "stable", "releases/old/bin", "units", "image", "slow-bin"} {
		if err := os.MkdirAll(filepath.Join(scratch, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: args, Workspace: root, Timeout: 8 * time.Minute})
		return out.Output, out.RunErr
	}
	run := func(args ...string) (string, error) {
		out, err := command(ctx, args...)
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", args[0], err, out)
		}
		return strings.TrimSpace(out)
	}
	for _, build := range []struct{ path, pkg string }{{"stable/apptown-update", "./cmd/apptown-update"}, {"releases/old/bin/apptownd", "./internal/apptown/instanceupdate/supervisor/testdata/daemon"}} {
		out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: []string{"go", "build", "-o", filepath.Join(scratch, build.path), build.pkg}, Workspace: root, Env: []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + runtime.GOARCH}, Timeout: 8 * time.Minute})
		if out.RunErr != nil {
			t.Fatalf("build probe: %v %s", out.RunErr, out.Output)
		}
	}
	job := strings.TrimPrefix(filepath.Base(os.TempDir()), "apptown-ci-")
	if job == filepath.Base(os.TempDir()) || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(job) {
		t.Fatal("adapter proof requires the maintained ci-heavy temporary root")
	}
	name := "apptown-u2-" + job + "-" + adapter
	image := name + ":test"
	var ownedVolume, kernelBefore string
	kernelSnapshot := func(ctx context.Context) (string, error) {
		out, err := command(ctx, "docker", "run", "--rm", "--name", name+"-kernel",
			"--network=none", "--read-only", "--cap-drop=ALL",
			"--mount", "type=bind,src=/proc/sys/fs/binfmt_misc,dst=/observed,readonly",
			"--entrypoint", "sh", image, "-c", `set -eu; printf 'kernel-binfmt-v1\n'; for f in /observed/*; do
[ "${f##*/}" = register ] && continue
[ -f "$f" ] || continue
printf '%s\n' "${f##*/}"
cat "$f"
done`)
		return string(out), err
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if out, err := command(cleanup, "docker", "image", "rm", image); err != nil {
			if !strings.Contains(string(out), "No such image") {
				t.Errorf("remove owned image: %v %s", err, out)
			}
		}
	})
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if out, err := command(cleanup, "docker", "rm", "-f", "-v", name); err != nil {
			if !strings.Contains(string(out), "No such container") {
				t.Errorf("settle owned host: %v %s", err, out)
			}
		} else {
			t.Log("owned systemd host removed; its PID namespace and nested children settled")
			if ownedVolume != "" {
				volumes, err := command(cleanup, "docker", "volume", "ls", "--format", "{{.Name}}", "--filter", "name="+ownedVolume)
				if err != nil {
					t.Errorf("observe owned volume settlement: %v %s", err, volumes)
				}
				for _, name := range strings.Fields(string(volumes)) {
					if name == ownedVolume {
						t.Error("owned native volume remains after host removal")
					}
				}
			}
		}
		if kernelBefore != "" {
			after, err := kernelSnapshot(cleanup)
			if err != nil || after != kernelBefore {
				t.Errorf("shared binfmt registrations changed or unavailable after fixture settlement: %v\nbefore:\n%s\nafter:\n%s", err, kernelBefore, after)
			} else {
				t.Log("shared binfmt registrations unchanged after fixture settlement")
			}
		}
	})
	must("docker", "build", "--platform=linux/"+runtime.GOARCH, "-t", image, "-f", filepath.Join(root, "internal/apptown/instanceupdate/supervisor/testdata/host.Dockerfile"), filepath.Join(root, "internal/apptown/instanceupdate/supervisor/testdata"))
	kernelBefore, err = kernelSnapshot(ctx)
	if err != nil || !strings.HasPrefix(kernelBefore, "kernel-binfmt-v1\n") {
		t.Fatalf("cannot establish shared binfmt baseline before fixture boot: %v %s", err, kernelBefore)
	}
	must("docker", "run", "-d", "--name", name, "--privileged", "--cgroupns=private", "--network=none", "--tmpfs", "/run", "--tmpfs", "/run/lock", "--tmpfs", "/tmp", "-e", "container=docker", "-v", scratch+":/input:ro", "-v", "/u2", image)
	ownedVolume = must("docker", "inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/u2"}}{{.Name}}{{end}}{{end}}`, name)
	if ownedVolume == "" {
		t.Fatal("fixture has no native owned volume")
	}
	inside := func(args ...string) string { return must(append([]string{"docker", "exec", name}, args...)...) }
	poll := func(label string, f func() bool) {
		t.Helper()
		if !polling.Until(ctx, 30*time.Second, 100*time.Millisecond, f) {
			logs, _ := run("docker", "logs", name)
			t.Fatalf("%s did not settle\n%s", label, logs)
		}
	}
	poll("systemd PID1", func() bool {
		out, err := run("docker", "exec", name, "systemctl", "show", "--property=Version")
		return err == nil && strings.Contains(out, "Version=")
	})
	if path := inside("systemctl", "show", "--value", "--property=UnitPath"); path != "/etc/systemd/system" {
		t.Fatal("fixture manager broadened unit search path", path)
	}
	managerEnv := inside("cat", "/proc/1/environ")
	for _, want := range []string{"SYSTEMD_UNIT_PATH=/etc/systemd/system", "SYSTEMD_GENERATOR_PATH=/etc/apptown-u2/no-generators", "SYSTEMD_ENVIRONMENT_GENERATOR_PATH=/etc/apptown-u2/no-generators"} {
		found := false
		for _, entry := range strings.Split(managerEnv, "\x00") {
			found = found || entry == want
		}
		if !found {
			t.Fatal("fixture manager lacks replacement search path", want)
		}
	}
	inside("sh", "/usr/local/lib/apptown-u2/verify-host.sh")
	for _, line := range strings.Split(inside("systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && fields[0] != "systemd-journald.service" {
			t.Fatal("unexpected service in minimal boot graph", line)
		}
	}
	hostName := name
	inside("cp", "-a", "/input/.", "/u2/")
	inside("chown", "-R", "0:0", "/u2")
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(scratch, name), body, 0600); err != nil {
			t.Fatal(err)
		}
		must("docker", "cp", filepath.Join(scratch, name), hostName+":/u2/"+name)
	}
	r := Registration{Format: Protocol, Instance: ids.New(time.Now()), DataDir: "/u2/data", StateDir: "/u2/state", DatabaseIdentity: strings.Repeat("a", 64), Supervisor: "/u2/stable/apptown-update", Adapter: adapter, Service: "apptown-u2.service", EnvironmentFile: "/u2/daemon.env", ReleaseDir: "/u2/releases/old", EnrolledAt: time.Now()}
	// Hash installed bytes inside the registered host. The outer staging root
	// belongs to the CI runner, not the installation's administrator.
	hash := func(path string) string { return strings.Fields(inside("sha256sum", path))[0] }
	r.SupervisorSHA256 = hash(r.Supervisor)
	r.ExecutableSHA256 = hash("/u2/releases/old/bin/apptownd")
	env := "APPTOWN_DATA=/u2/data\nAPPTOWN_UPDATE_SUPERVISOR_SOCKET=/u2/state/control.sock\n"
	if adapter == "docker" {
		env += "U2_STOP_DELAY=61\nPATH=/u2/slow-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n"
		wrapper := []byte(`#!/bin/sh
case " $* " in *" info "*|*" inspect "*) if [ -f /u2/delay-observation ]; then sleep 2; fi ;; esac
exec /usr/local/bin/docker "$@"
`)
		if err := execfile.Write(filepath.Join(scratch, "slow-bin/docker"), wrapper, 0700); err != nil {
			t.Fatal(err)
		}
		inside("cp", "/input/slow-bin/docker", "/u2/slow-bin/docker")
	}
	write("daemon.env", []byte(env))
	r.EnvironmentSHA256 = hash("/u2/daemon.env")
	var neighbor string
	if adapter == "docker" {
		write("units/docker.service", []byte("[Unit]\nDescription=Owned test Docker\n[Service]\nExecStart=/usr/local/bin/dockerd --host unix:///run/u2-docker.sock --data-root=/u2/docker --storage-driver=vfs --iptables=false --bridge=none\nDelegate=yes\nKillMode=process\n[Install]\nWantedBy=multi-user.target\n"))
		inside("cp", "/u2/units/docker.service", "/etc/systemd/system/docker.service")
		inside("systemctl", "daemon-reload")
		inside("systemctl", "enable", "--now", "docker.service")
		poll("owned Docker", func() bool {
			_, err := run("docker", "exec", name, "docker", "--host", "unix:///run/u2-docker.sock", "info")
			return err == nil
		})
		inside("cp", "/u2/releases/old/bin/apptownd", "/u2/image/daemon")
		write("image/Dockerfile", []byte("FROM scratch\nCOPY daemon /daemon\nENTRYPOINT [\"/daemon\"]\n"))
		dk := func(args ...string) string {
			return inside(append([]string{"docker", "--host", "unix:///run/u2-docker.sock"}, args...)...)
		}
		dk("build", "--platform=linux/"+runtime.GOARCH, "-t", "u2-probe:local", "/u2/image")
		r.Container = dk("create", "--restart=no", "--network=host", "--env-file", "/u2/daemon.env", "-v", "/u2/data:/u2/data", "-v", "/u2/state:/u2/state", "u2-probe:local")
		r.Image = dk("inspect", "--format", "{{.Image}}", r.Container)
		r.DockerEngine = dk("info", "--format", "{{.ID}}")
		r.DockerSocket = "/run/u2-docker.sock"
		r.ReleaseDir = ""
		r.ExecutableSHA256 = ""
		// A separate copy of the static fixture runs without the managed socket and
		// waits; stopping the target must not select this neighbor by image/name.
		neighbor = dk("create", "--restart=no", "--network=none", "-e", "U2_NEIGHBOR=true", "u2-probe:local")
		dk("start", neighbor)
	}
	unit, err := ManagedUnit(r)
	if err != nil {
		t.Fatal(err)
	}
	write("units/daemon.service", unit)
	r.UnitSHA256 = hash("/u2/units/daemon.service")
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	write("registration-input.json", body)
	write("units/supervisor.service", []byte("[Unit]\nDescription=Owned update supervisor\nAfter=docker.service\n[Service]\nExecStart=/u2/stable/apptown-update -state /u2/state serve\nRestart=on-failure\nRestartSec=1\n[Install]\nWantedBy=multi-user.target\n"))
	inside("cp", "/u2/units/daemon.service", "/etc/systemd/system/apptown-u2.service")
	inside("cp", "/u2/units/supervisor.service", "/etc/systemd/system/apptown-update-u2.service")
	inside("systemctl", "daemon-reload")
	inside("/u2/stable/apptown-update", "-state", "/u2/state", "enroll", "/u2/registration-input.json")
	inside("systemctl", "enable", "--now", "apptown-update-u2.service")
	inside("systemctl", "enable", "--now", "apptown-u2.service")
	poll("actual registered peer launch", func() bool {
		_, err := run("docker", "exec", name, "test", "-f", "/u2/data/boot.json")
		return err == nil
	})
	boot := inside("cat", "/u2/data/boot.json")
	var status Status
	if err := json.Unmarshal([]byte(inside("/u2/stable/apptown-update", "-state", "/u2/state", "status")), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.Eligible || status.Phase != "staged" {
		t.Fatalf("incorrect actual status: %+v", status)
	}
	inside("systemctl", "restart", "apptown-update-u2.service")
	poll("supervisor restart status", func() bool {
		out, err := run("docker", "exec", name, "/u2/stable/apptown-update", "-state", "/u2/state", "status")
		return err == nil && strings.Contains(out, "staged")
	})
	if adapter == "docker" {
		write("delay-observation", []byte("delay only this owned controller's observations"))
	}
	inside("systemctl", "stop", "apptown-u2.service")
	if adapter == "docker" {
		if code := inside("systemctl", "show", "--value", "--property=ExecMainCode", "apptown-u2.service"); code != "1" {
			t.Fatal("systemd killed controller before bounded settlement", code)
		}
		inside("rm", "/u2/delay-observation")
	}
	if adapter == "docker" {
		if got := inside("docker", "--host", "unix:///run/u2-docker.sock", "inspect", "--format", "{{.State.Running}}", r.Container); got != "false" {
			t.Fatal("target did not stop", got)
		}
		if got := inside("docker", "--host", "unix:///run/u2-docker.sock", "inspect", "--format", "{{.State.Running}}", neighbor); got != "true" {
			t.Fatal("neighbor did not survive target stop", got)
		}
	}
	// Exercise current configuration drift through the actual enabled guard.
	originalEnv := []byte(env)
	write("daemon.env", append(append([]byte{}, originalEnv...), []byte("# changed after enrollment\n")...))
	inside("systemctl", "start", "apptown-u2.service")
	poll("environment drift refused before child", func() bool {
		out, err := run("docker", "exec", name, "journalctl", "-u", "apptown-u2.service", "--no-pager", "-o", "cat")
		return err == nil && strings.Contains(out, "registered artifact differs: /u2/daemon.env")
	})
	inside("systemctl", "stop", "apptown-u2.service")
	write("daemon.env", originalEnv)
	if changed := inside("cat", "/u2/data/boot.json"); changed != boot {
		t.Fatal("configuration drift launched the daemon")
	}
	inside("systemctl", "stop", "apptown-update-u2.service")
	// No U2 API enters maintenance. Inject the future coordinator's durable phase
	// only after both service owners settled, to prove boot cannot bypass it.
	var j journal
	raw := []byte(inside("cat", "/u2/state/journal.json"))
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Transactions) != 1 {
		t.Fatal("actual peer request was not retained", len(j.Transactions))
	}
	j.Transactions[0].Phase = "verifying"
	raw, err = json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	write("state/journal.json", raw)
	offline, err := run("docker", "exec", name, "/u2/stable/apptown-update", "-state", "/u2/state", "recover")
	if err == nil || !strings.Contains(offline, "supervisor-unavailable") || !strings.Contains(offline, "ordinary startup is fenced") || !strings.Contains(offline, "unchanged") {
		t.Fatalf("offline recovery did not preserve truthful refusal: %v %s", err, offline)
	}
	if after := inside("cat", "/u2/state/journal.json"); after != string(raw) {
		t.Fatal("offline recovery changed maintenance evidence")
	}
	must("docker", "restart", name)
	poll("enabled guard attempted and refused after reboot", func() bool {
		out, err := run("docker", "exec", name, "journalctl", "-u", "apptown-u2.service", "--no-pager", "-o", "cat")
		return err == nil && strings.Contains(out, "ordinary startup is fenced")
	})
	after := inside("cat", "/u2/data/boot.json")
	if after != boot {
		t.Fatal("normal daemon launched through maintenance after reboot")
	}
	if adapter == "docker" {
		poll("owned Docker after reboot", func() bool {
			_, err := run("docker", "exec", name, "docker", "--host", "unix:///run/u2-docker.sock", "info")
			return err == nil
		})
		if got := inside("docker", "--host", "unix:///run/u2-docker.sock", "inspect", "--format", "{{.State.Running}}", r.Container); got != "false" {
			t.Fatal("target bypassed reboot guard", got)
		}
	}
	t.Logf("%s: real service/peer, durable request, supervisor restart, exact stop and enabled reboot fence observed", adapter)
}
