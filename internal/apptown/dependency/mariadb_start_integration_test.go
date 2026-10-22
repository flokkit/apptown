//go:build integration

package dependency

import (
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// The stock MariaDB need makes its data folder, and the app's database and
// user, itself. Its first start does not use the image's own setup, which
// waits about 30 seconds for a temporary server and gives up on a busy
// host (22 Oct: on a loaded CI runner MariaDB exited "Unable to start
// server"; restarted, it came up healthy without the app's database). A data
// folder that lacks the app's database and user, as that left one, has them
// after the next start.
func TestIntegrationTheMariaDBNeedMakesTheAppsDatabaseWithoutTheImagesSetup(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/mariadb.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for name, creds := range map[string]map[string]string{
		"the catalogue's own": nil,
		// Quotes and backslashes reach SQL as written: the start script
		// quotes them, and nothing on the way reads them again.
		"awkward ones": {"MARIADB_PASSWORD": `p'ass\word"$x`, "MARIADB_DATABASE": "odd`name\\x_y%z"},
	} {
		t.Run(name, func(t *testing.T) { mariadbStartsItself(t, kind, creds) })
	}
}

func mariadbStartsItself(t *testing.T, kind *Kind, creds map[string]string) {
	spec := kind.ResolveLocal(nil, "", nil).Spec()
	env := maps.Clone(spec.Env)
	maps.Copy(env, creds)
	spec.Env = env
	stack := fmt.Sprintf("env-mariadbstart%d-%d-main", os.Getpid(), time.Now().UnixNano())
	name := docker.Current.Service(stack, spec.Name)
	dk := docker.Scope{Client: docker.CLI{Binary: "docker"}}
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", name)
		_ = dockertest.Run("volume", "rm", docker.Current.NeedVolume(stack, spec.Name))
		_ = dockertest.Run("network", "rm", docker.Current.Network(stack))
	})
	own := docker.Ownership{Record: func(deploy.Owned) error { return nil }, Needs: map[string]string{spec.Name: spec.Data}}
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{spec}, "production", own); err != nil {
		t.Fatal(err)
	}
	database := env["MARIADB_DATABASE"]
	// The app signs in with its password as given and makes a table in its
	// database, named as given: a name the script changed would be refused
	// as an unknown database.
	appUses := func() {
		t.Helper()
		var out []byte
		var err error
		if !poll.Until(t.Context(), 2*time.Minute, 500*time.Millisecond, func() bool {
			out, err = dockertest.CombinedOutput("exec", name, "mariadb", "-h127.0.0.1", "--skip-ssl", "-u"+env["MARIADB_USER"], "-p"+env["MARIADB_PASSWORD"], "-e",
				"CREATE TABLE IF NOT EXISTS seen (n int)", database)
			return err == nil
		}) {
			state, _ := dockertest.CombinedOutput("inspect", "-f", "{{json .State}}", name)
			logs, _ := dockertest.CombinedOutput("logs", "--tail", "80", name)
			t.Fatalf("the app cannot use its database: %v %s\nits state: %s\nits log:\n%s", err, out, state, logs)
		}
	}
	appUses()
	logs, _ := dockertest.CombinedOutput("logs", name)
	if strings.Contains(string(logs), "temporary server") {
		t.Fatalf("the first start used the image's own setup:\n%s", logs)
	}
	// What an image setup that gave up leaves: a data folder without the
	// app's database or user. Root signs in over the socket inside.
	drop := "DROP DATABASE `" + strings.ReplaceAll(database, "`", "``") + "`; DROP USER '" + env["MARIADB_USER"] + "'@'%'"
	if out, err := dockertest.CombinedOutput("exec", name, "mariadb", "-e", drop); err != nil {
		t.Fatalf("removing the app's database and user: %v %s", err, out)
	}
	if out, err := dockertest.CombinedOutput("restart", name); err != nil {
		t.Fatalf("restarting: %v %s", err, out)
	}
	appUses()
}
