package agentenv

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// CLI locates the Linux CLI supplied with the daemon. It never builds source at
// runtime; deployment packaging owns the executable used inside containers.
type CLI struct {
	once sync.Once
	path string
	err  error
}

// WithCLIPath keeps the mounted CLI ahead of toolchain shims. Without ambient
// registry configuration, use the supported agent image's standard search path.
// Preserve an explicitly supplied PATH, including custom image/tool locations.
func WithCLIPath(env []string) []string {
	env = slices.Clone(env)
	found := false
	for i, value := range env {
		if path, ok := strings.CutPrefix(value, "PATH="); ok {
			found = true
			if !strings.HasPrefix(path, CLIDir+":") && path != CLIDir {
				env[i] = "PATH=" + CLIDir + ":" + path
			}
		}
	}
	if !found {
		env = append(env, "PATH="+CLIDir+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	return env
}

// NewCLI anchors an explicit path at construction. An empty path selects the
// forge-linux artifact beside the running daemon, independent of its workdir.
func NewCLI(path string) *CLI {
	var err error
	if path == "" {
		var executable string
		executable, err = os.Executable()
		if err == nil {
			path = filepath.Join(filepath.Dir(executable), "forge-linux")
		}
	} else {
		path, err = filepath.Abs(path)
	}
	return &CLI{path: path, err: err}
}

// Path checks availability once per process. Failure remains nonfatal: sessions
// run without the CLI, and the session view reports its absence.
func (cli *CLI) Path() (string, error) {
	cli.once.Do(func() {
		if cli.err == nil {
			var info os.FileInfo
			info, cli.err = os.Stat(cli.path)
			if cli.err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0) {
				cli.err = fmt.Errorf("%s is not an executable file", cli.path)
			}
		}
		if cli.err != nil {
			cli.err = fmt.Errorf("console CLI unavailable (build with make console-cli or set -console-cli): %w", cli.err)
			log.Printf("console: %v — sessions will run WITHOUT the forge CLI", cli.err)
		}
	})
	if cli.err != nil {
		return "", cli.err
	}
	return cli.path, nil
}
