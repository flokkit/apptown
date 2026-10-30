// Package diskspacetest provides an owned fake daemon for disk probes.
package diskspacetest

import (
	"slices"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Daemon answers only disk commands using the shared result fake. Each test
// daemon owns an instance; unrelated result consumers remain unclaimed.
type Daemon struct {
	Answer  func() containerresulttest.Answer
	once    sync.Once
	results containerresulttest.Daemon
}

// Started counts the containers the disk reads started: the standing
// reader, made or started again.
func (d *Daemon) Started() int { return d.results.Started }

// IsCreate recognizes the making of a daemon's standing disk reader.
func IsCreate(args []string) bool {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	return len(args) > 4 && args[0] == "run" && args[1] == "-d" && slices.Contains(args, dockerlabel.Class+"="+diskspace.ReaderClass)
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
		d.results.Answers = func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
			if c.Script != "df -Pk /d" {
				return containerresulttest.Answer{}, false
			}
			if d.Answer != nil {
				return d.Answer(), true
			}
			return containerresulttest.Answer{Answer: []byte(RoomyAnswer)}, true
		}
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
