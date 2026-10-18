package main

import (
	"flag"
	"strings"
	"testing"
)

// Every flag reads its FORGE_ environment variable, the names forged has
// always read keep their spelling, and a flag on the command line wins.
func TestEveryFlagHasAnEnvironmentBinding(t *testing.T) {
	// The environment names deployments already set (systemd drop-ins, the
	// quickstart) must not change.
	for flagName, env := range map[string]string{
		"db-url": "FORGE_DB_URL", "console-cli": "FORGE_CONSOLE_CLI", "inference-addr": "FORGE_INFERENCE_ADDR",
		"inference-url": "FORGE_INFERENCE_URL", "inference-networks": "FORGE_INFERENCE_NETWORKS",
		"mirror-base": "FORGE_MIRROR_BASE", "features": "FORGE_FEATURES", "otlp-endpoint": "FORGE_OTLP_ENDPOINT",
		"otlp-protocol": "FORGE_OTLP_PROTOCOL", "errors-url": "FORGE_ERRORS_URL",
	} {
		if got := envName(flagName); got != env {
			t.Fatalf("%s reads %s, want %s", flagName, got, env)
		}
	}
	fs := flag.NewFlagSet("forged", flag.ContinueOnError)
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
		case "verify-only", "no-private-docker", "private-docker", "no-local-docker", "drain-local-docker", "git-import-allow-private", "backfill-recorded-main", "open-on-network", "host-scripts":
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
	fs = flag.NewFlagSet("forged", flag.ContinueOnError)
	defineFlags(fs)
	_ = fs.Parse(nil)
	err := bindEnv(fs, func(k string) string { return map[string]string{"FORGE_AGENT_CONCURRENCY": "lots"}[k] })
	if err == nil || !strings.Contains(err.Error(), "FORGE_AGENT_CONCURRENCY") {
		t.Fatalf("malformed value: %v", err)
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

// -portainer-environment reaches the forge's configuration, local unless
// stated; FORGE_PORTAINER_ENVIRONMENT states it as well.
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
		{nil, map[string]string{"FORGE_PORTAINER_ENVIRONMENT": "office"}, "office"},
	} {
		fs := flag.NewFlagSet("forged", flag.ContinueOnError)
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
