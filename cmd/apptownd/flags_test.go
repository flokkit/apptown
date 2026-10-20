package main

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every flag reads its APPTOWN_ environment variable, the names apptownd has
// always read keep their spelling, and a flag on the command line wins.
func TestEveryFlagHasAnEnvironmentBinding(t *testing.T) {
	// The environment names deployments already set (systemd drop-ins, the
	// quickstart) must not change.
	for flagName, env := range map[string]string{
		"db-url": "APPTOWN_DB_URL", "console-cli": "APPTOWN_CONSOLE_CLI", "inference-addr": "APPTOWN_INFERENCE_ADDR",
		"inference-url": "APPTOWN_INFERENCE_URL", "inference-networks": "APPTOWN_INFERENCE_NETWORKS",
		"mirror-base": "APPTOWN_MIRROR_BASE", "features": "APPTOWN_FEATURES", "otlp-endpoint": "APPTOWN_OTLP_ENDPOINT",
		"otlp-protocol": "APPTOWN_OTLP_PROTOCOL", "errors-url": "APPTOWN_ERRORS_URL",
	} {
		if got := envName(flagName); got != env {
			t.Fatalf("%s reads %s, want %s", flagName, got, env)
		}
	}
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	o := defineFlags(fs)
	env := map[string]string{}
	count := 0
	fs.VisitAll(func(f *flag.Flag) {
		count++
		v := "from-env"
		switch f.Name {
		case "agent-concurrency":
			v = "3"
		case "import-bundle-max-bytes":
			v = "1048576"
		case "task-cpus", "chat-cpus":
			v = "1.5"
		case "task-memory-mib", "task-pids", "chat-memory-mib", "chat-pids":
			v = "512"
		case "verify-only", "no-private-docker", "private-docker", "no-local-docker", "local-tls", "drain-local-docker", "git-import-allow-private", "backfill-recorded-main", "open-on-network", "host-scripts":
			v = "true"
		case "tier-domain":
			v = "preview=p.example,main=m.example"
		}
		env[envName(f.Name)] = v
	})
	if err := fs.Parse([]string{"-addr", ":9999"}); err != nil {
		t.Fatal(err)
	}
	if err := bindEnv(fs, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	fs.VisitAll(func(f *flag.Flag) {
		want := env[envName(f.Name)]
		switch f.Name {
		case "addr":
			want = ":9999" // the command line wins
		case "tier-domain":
			return // checked below
		}
		if f.Value.String() != want {
			t.Fatalf("%s: %q, want %q from %s", f.Name, f.Value.String(), want, envName(f.Name))
		}
	})
	if count < 25 || o.agentConc != 3 || !o.verifyOnly || strings.Join(o.tierDomains, " ") != "preview=p.example main=m.example" {
		t.Fatalf("%d flags; concurrency %d, verify %v, tiers %v", count, o.agentConc, o.verifyOnly, o.tierDomains)
	}
	// A malformed value names the variable instead of starting half-configured.
	fs = flag.NewFlagSet("apptownd", flag.ContinueOnError)
	defineFlags(fs)
	_ = fs.Parse(nil)
	err := bindEnv(fs, func(k string) string { return map[string]string{"APPTOWN_AGENT_CONCURRENCY": "lots"}[k] })
	if err == nil || !strings.Contains(err.Error(), "APPTOWN_AGENT_CONCURRENCY") {
		t.Fatalf("malformed value: %v", err)
	}
}

// A setting still spelled the old way (FORGE_DB_URL) stops the daemon with
// the new spelling named, rather than starting on defaults: an env file half
// moved to the AppTown names would otherwise lose its settings silently.
func TestAnOldSettingNameStopsTheDaemon(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	defineFlags(fs)
	_ = fs.Parse(nil)
	err := bindEnv(fs, func(k string) string { return map[string]string{"FORGE_DB_URL": "postgres://x"}[k] })
	if err == nil || !strings.Contains(err.Error(), "FORGE_DB_URL") || !strings.Contains(err.Error(), "APPTOWN_DB_URL") {
		t.Fatalf("old setting: %v", err)
	}
}

// The API servers cut off a client that sends its headers slowly or keeps
// an idle connection, without a read or write timeout that would cut off a
// long upload or a chat's terminal.
func TestTheAPIServersCutOffSlowAndIdleClients(t *testing.T) {
	s := servedAt(":0", nil)
	if s.ReadHeaderTimeout == 0 || s.IdleTimeout == 0 || s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatalf("timeouts: header %v idle %v read %v write %v", s.ReadHeaderTimeout, s.IdleTimeout, s.ReadTimeout, s.WriteTimeout)
	}
}

// -portainer-environment reaches AppTown's configuration, local unless
// stated; APPTOWN_PORTAINER_ENVIRONMENT states it as well.
//
// Kills: the flag not carried into forge.Config, so live's environment name
// would be ignored and the default used.
func TestThePortainerEnvironmentFlagReachesTheForgeConfig(t *testing.T) {
	for _, c := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{nil, nil, "local"},
		{[]string{"-portainer-environment", "office"}, nil, "office"},
		{nil, map[string]string{"APPTOWN_PORTAINER_ENVIRONMENT": "office"}, "office"},
	} {
		fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
		o := defineFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if err := bindEnv(fs, func(k string) string { return c.env[k] }); err != nil {
			t.Fatal(err)
		}
		if got := o.forgeConfig("/data").PortainerEnvironment; got != c.want {
			t.Fatalf("%v %v: %q, want %q", c.args, c.env, got, c.want)
		}
	}
}

// private/scripts/deploy-ct.sh refuses a CT whose settings still use an old
// FORGE_<FLAG> name, and only those (the CLI's FORGE_ variables may stay).
// Its list is this daemon's flags, so a new flag cannot slip past it.
func TestTheCTDeployKnowsEveryDaemonSetting(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("..", "..", "private", "scripts", "deploy-ct.sh"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("the maintainers' deploy script is not in the public tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, line := range strings.Split(string(src), "\n") {
		if v, ok := strings.CutPrefix(line, "daemon_settings="); ok {
			listed = strings.Split(strings.Trim(v, `"`), "|")
		}
	}
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	defineFlags(fs)
	var want []string
	fs.VisitAll(func(f *flag.Flag) { want = append(want, settingName(f.Name)) })
	slices.Sort(listed)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Fatalf("deploy-ct.sh's daemon_settings:\n%v\nthe daemon's flags:\n%v", listed, want)
	}
}
