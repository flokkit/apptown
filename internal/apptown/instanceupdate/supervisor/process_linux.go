//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processUID(pid int) (uint32, error) {
	st, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return 0, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("process owner unavailable")
	}
	return owner.Uid, nil
}
func managedChild(pid int, r Registration) (Observation, error) {
	guard, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return Observation{}, err
	}
	if guard != r.Supervisor {
		return Observation{}, errors.New("service bypassed stable startup guard")
	}
	// Children belong to the task that created them. Go may launch the daemon
	// from any OS thread; the leader's children file is not process-wide.
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return Observation{}, err
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Observation{}, err
		}
		for _, child := range strings.Fields(string(raw)) {
			seen[child] = true
		}
	}
	children := make([]string, 0, len(seen))
	for child := range seen {
		children = append(children, child)
	}
	if len(children) != 1 {
		return Observation{}, errors.New("registered guard has no unique daemon child")
	}
	child, err := strconv.Atoi(children[0])
	if err != nil {
		return Observation{}, err
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", child))
	if err != nil {
		return Observation{}, err
	}
	if exe != filepath.Join(r.ReleaseDir, "bin", "apptownd") {
		return Observation{}, errors.New("registered daemon executable changed")
	}
	uid, err := processUID(child)
	if err != nil {
		return Observation{}, err
	}
	if uid != r.DaemonUID {
		return Observation{}, errors.New("registered daemon user changed")
	}
	return Observation{PID: child, UID: uid, Running: true}, nil
}
