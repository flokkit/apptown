package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/flagenv"
)

// Every flag reads its APPTOWN_ environment variable, the names apptownd has
// always read keep their spelling, and a flag on the command line wins.
func TestEveryFlagHasAnEnvironmentBinding(t *testing.T) {
	// The environment names deployments already set (systemd drop-ins, the
	// quickstart) must not change.
	for flagName, env := range map[string]string{
		"db-url": "APPTOWN_DB_URL", "console-cli": "APPTOWN_CONSOLE_CLI", "inference-addr": "APPTOWN_INFERENCE_ADDR",
		"inference-url": "APPTOWN_INFERENCE_URL", "inference-networks": "APPTOWN_INFERENCE_NETWORKS",
		"otlp-endpoint": "APPTOWN_OTLP_ENDPOINT", "otlp-protocol": "APPTOWN_OTLP_PROTOCOL", "disable-reap": "APPTOWN_DISABLE_REAP",
	} {
		if got := flagenv.Name(flagName); got != env {
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
		case "import-bundle-max-bytes":
			v = "1048576"
		case "task-cpus", "chat-cpus":
			v = "1.5"
		case "task-memory-mib", "task-pids", "chat-memory-mib", "chat-pids":
			v = "512"
		case "update-checks", "verify-only", "disable-reap", "no-private-docker", "private-docker", "no-local-docker", "local-tls", "git-import-allow-private", "open-on-network", "host-scripts":
			v = "true"
		case "tier-domain":
			v = "preview=p.example,main=m.example"
		}
		env[flagenv.Name(f.Name)] = v
	})
	if err := fs.Parse([]string{"-addr", ":9999"}); err != nil {
		t.Fatal(err)
	}
	if err := bindEnv(fs, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	fs.VisitAll(func(f *flag.Flag) {
		want := env[flagenv.Name(f.Name)]
		switch f.Name {
		case "addr":
			want = ":9999" // the command line wins
		case "tier-domain":
			return // checked below
		}
		if f.Value.String() != want {
			t.Fatalf("%s: %q, want %q from %s", f.Name, f.Value.String(), want, flagenv.Name(f.Name))
		}
	})
	if count < 25 || o.taskPIDs != 512 || !o.verifyOnly || !o.apptownConfig("/data").DisableReap || strings.Join(o.tierDomains, " ") != "preview=p.example main=m.example" {
		t.Fatalf("%d flags; pids %d, verify %v, disable-reap %v, tiers %v", count, o.taskPIDs, o.verifyOnly, o.disableReap, o.tierDomains)
	}
	// A malformed value names the variable instead of starting half-configured.
	fs = flag.NewFlagSet("apptownd", flag.ContinueOnError)
	defineFlags(fs)
	_ = fs.Parse(nil)
	err := bindEnv(fs, func(k string) string { return map[string]string{"APPTOWN_DISABLE_REAP": "on"}[k] })
	if err == nil || !strings.Contains(err.Error(), "APPTOWN_DISABLE_REAP") {
		t.Fatalf("malformed value: %v", err)
	}
}

// Cleanup removes leftovers unless -disable-reap is given, and the old
// -reap is not a flag: a command line that still passes it stops apptownd
// rather than starting with a setting it no longer means.
//
// Kills: cleanup off by default, or -reap kept as an alias.
func TestCleanupIsOnUnlessDisabled(t *testing.T) {
	t.Parallel()
	parse := func(args ...string) (*daemonFlags, error) {
		fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		o := defineFlags(fs)
		return o, fs.Parse(args)
	}
	o, err := parse()
	if err != nil || o.apptownConfig("/data").DisableReap {
		t.Fatalf("no flags: err %v, cleanup disabled %v", err, o.disableReap)
	}
	if o, err = parse("-disable-reap"); err != nil || !o.apptownConfig("/data").DisableReap {
		t.Fatalf("-disable-reap: err %v, cleanup disabled %v", err, o.disableReap)
	}
	if _, err = parse("-reap"); err == nil {
		t.Fatal("-reap was accepted")
	}
}

// A FORGE_ variable is not a setting: apptownd starts, reading only the
// APPTOWN_ names.
func TestAnAppTownVariableIsNotASetting(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	o := defineFlags(fs)
	_ = fs.Parse(nil)
	if err := bindEnv(fs, func(k string) string { return map[string]string{"FORGE_DB_URL": "postgres://x"}[k] }); err != nil {
		t.Fatalf("a FORGE_ variable stopped apptownd: %v", err)
	}
	if o.dbURL != "" {
		t.Fatalf("FORGE_DB_URL was read: %q", o.dbURL)
	}
}

// APPTOWN_VERSION names a release for try.sh and the one-server compose
// file, and so reaches apptownd's environment: it neither stops apptownd
// nor makes it print its version and exit.
func TestTheVersionVariableIsNotASetting(t *testing.T) {
	t.Parallel()
	fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
	_, showVersion := parseDaemonFlags(fs, nil)
	env := map[string]string{"APPTOWN_VERSION": "1.0.0"}
	if err := bindEnv(fs, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if showVersion || fs.Lookup("version").Value.String() != "false" {
		t.Fatalf("APPTOWN_VERSION set -version")
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

// The settings an administrator now changes in AppTown itself, or that it
// no longer has, are not flags: a command line that still passes one stops
// apptownd at start instead of being quietly ignored.
func TestSettingsMovedIntoAppTownAreNotFlags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"agent-model", "agent-concurrency", "errors-url", "mirror-base", "portainer-environment"} {
		fs := flag.NewFlagSet("apptownd", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		defineFlags(fs)
		if err := fs.Parse([]string{"-" + name, "x"}); err == nil {
			t.Errorf("-%s is still a flag", name)
		}
	}
}

func TestUpdateDiscoveryDefaultsAndExplicitPrereleaseOptIn(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	defaults := defineFlags(fs)
	if defaults.updateChannel != "stable" || !defaults.updateChecks {
		t.Fatal("release discovery defaults changed")
	}
	if err := fs.Parse([]string{"-update-channel=prerelease", "-update-checks=false"}); err != nil {
		t.Fatal(err)
	}
	if defaults.apptownConfig(t.TempDir()).UpdateChannel != "prerelease" || defaults.updateChecks {
		t.Fatal("explicit channel/disable lost")
	}
}
