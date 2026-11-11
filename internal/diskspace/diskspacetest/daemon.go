// Package diskspacetest provides an owned fake daemon for the disk and kernel reader.
package diskspacetest

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Daemon answers disk and kernel commands using the shared result fake. Each test
// daemon owns an instance; unrelated result consumers remain unclaimed.
type Daemon struct {
	// Cgroup scripts cgroup answers by file name (scope selects cgroup, proc or v1).
	// Nil describes a whole machine with a cgroup v2 root.
	Cgroup func(string) (int, []byte, string)
	// Kernel scripts /proc counter answers; nil reports 8 GiB, 8 cores and 40% busy.
	// Uptime scripts the two host uptime readings; nil advances by one second.
	Uptime  func() string
	Kernel  func(string) (int, []byte, string)
	samples int
	Answer  func() (int, []byte, string)
	once    sync.Once
	results containerresulttest.Daemon
}

// Started counts the containers the disk reads started: the standing
// reader, made or started again.
func (d *Daemon) Started() int {
	return d.results.StartedCount()
}

// IsCreate recognizes the making of a daemon's standing disk reader: run -d,
// or create on a daemon reached by plain requests (containerresult.Spec.Plain).
func IsCreate(args []string) bool {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	made := len(args) > 4 && (args[0] == "run" && args[1] == "-d" || args[0] == "create")
	return made && slices.Contains(args, dockerlabel.Class+"="+diskspace.ReaderClass)
}

// isReader names a standing disk reader.
func isReader(name string) bool {
	return strings.HasPrefix(name, containerresult.StandingPrefix+diskspace.ReaderClass+"-")
}

func (d *Daemon) Run(args []string) ([]byte, error, bool) {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if len(args) == 6 && args[0] == "ps" && args[3] == "label="+dockerlabel.Class+"=disk-probe" {
		return nil, nil, true
	}
	d.once.Do(func() {
		d.results.Configure(func(image, container, script string, args, env []string) (int, []byte, string, bool) {
			if strings.HasPrefix(script, "section() {") {
				return d.kernelAnswer()
			}
			if script != "df -Pk /d" {
				return 0, nil, "", false
			}
			if d.Answer != nil {
				code, data, said := d.Answer()
				return code, data, said, true
			}
			return 0, []byte(RoomyAnswer), "", true
		})
	})
	return d.results.Run(args)
}

// Owns reports commands for the daemon's standing disk reader: its listing,
// making, starting and the reads in it.
func (d *Daemon) Owns(args []string) bool {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if IsCreate(args) {
		return true
	}
	if len(args) > 3 && args[0] == "ps" && slices.Contains(args, "label="+dockerlabel.Class+"="+diskspace.ReaderClass) {
		return true
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, ":")
		if isReader(name) {
			return true
		}
	}
	return false
}

// RoomyAnswer describes a filesystem with ample space for admission.
const RoomyAnswer = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/data 104857600 10485760 94371840 10% /d\n"

// Roomy also answers the daemon data-folder discovery used by disk admission.
func (d *Daemon) Roomy(args []string) ([]byte, error, bool) {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if len(args) == 3 && args[0] == "info" && strings.Contains(args[2], "DockerRootDir") {
		return []byte("8589934592\t8\t/var/lib/docker\n"), nil, true
	}
	return d.Run(args)
}

// IsRead includes the disk owner's discovery and diagnostic queries, alongside
// its owned result commands. It never classifies other containers' mutations.
func (d *Daemon) IsRead(args []string) bool {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if d.Owns(args) {
		return true
	}
	if len(args) == 3 && args[0] == "info" && args[1] == "--format" && strings.Contains(args[2], "DockerRootDir") {
		return true
	}
	if len(args) == 3 && args[0] == "version" && args[1] == "--format" && args[2] == "{{json .}}" {
		return true
	}
	if len(args) == 6 && args[0] == "ps" && args[1] == "-a" && args[2] == "--filter" && (args[3] == "label="+dockerlabel.Class+"=container-result" || args[3] == "label="+dockerlabel.Class+"=disk-probe") && args[4] == "--format" {
		return true
	}
	return false
}

func (d *Daemon) SetAnswer(answer func() (int, []byte, string)) { d.Answer = answer }

// kernelAnswer produces one labelled response, without waiting in the fake.
func (d *Daemon) kernelAnswer() (int, []byte, string, bool) {
	var answer strings.Builder
	section := func(name string, data []byte) {
		fmt.Fprintf(&answer, "== %s\n%s\n", name, strings.TrimSuffix(string(data), "\n"))
	}
	scope := "proc"
	if d.Cgroup != nil {
		code, data, said := d.Cgroup("scope")
		if code != 0 {
			return code, nil, said, true
		}
		scope = strings.TrimSpace(string(data))
	}
	section("scope", []byte(scope))
	for i := range 2 {
		suffix := "first"
		if i == 1 {
			suffix = "second"
		}
		uptime := fmt.Sprintf("%d.00 0.00", 100+i)
		if d.Uptime != nil {
			uptime = d.Uptime()
		}
		section("uptime."+suffix, []byte(uptime))
		files := []string{}
		if scope == "cgroup" {
			if i == 0 {
				files = append(files, "memory.current", "memory.stat", "memory.max")
			}
			files = append(files, "cpu.stat")
		} else if scope == "proc" {
			if i == 0 {
				files = append(files, "meminfo")
			}
			files = append(files, "stat")
		}
		for _, file := range files {
			var data []byte
			if scope == "cgroup" {
				_, data, _ = d.Cgroup(file)
			} else if d.Kernel != nil {
				_, data, _ = d.Kernel("cat /proc/" + file)
			} else if file == "meminfo" {
				data = []byte("MemTotal: 8388608 kB\nMemAvailable: 6291456 kB\n")
			} else {
				d.samples++
				data = fmt.Appendf(nil, "cpu %d 0 0 %d 0 0 0 0 0 0\n", d.samples*40, d.samples*60)
				for core := range 8 {
					data = fmt.Appendf(data, "cpu%d 0 0 0 0 0 0 0 0\n", core)
				}
			}
			name := file
			if file == "stat" || file == "cpu.stat" {
				name = "cpu." + suffix
			}
			section(name, data)
		}
	}
	return 0, []byte(answer.String()), "", true
}
