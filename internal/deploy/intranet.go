package deploy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// The intranet release (design/subsystems/lan-release.md): the stack file
// AppTown hands Portainer for one release of one app, and the plan a
// release runs from, with every built part pointed at the recorded image.
// Forge keeps the resolution of needs and secrets; this is the rendering.

// IntranetNeed is one fulfilled need as the stack runs it.
type IntranetNeed struct {
	Name    string
	Image   string
	Command []string
	// Entrypoint replaces the image's entrypoint (a need's start script).
	Entrypoint []string
	Env        map[string]string
	Health     string // a shell healthcheck (exit 0 = healthy); "" = none
	Data       string // the emulator's data directory; "" = ephemeral
	// Resources is the most the need's service may use; nothing declared
	// is no limit.
	Resources appcontract.Resources
}

// ConsumerSecrets is the resolved secrets per consumer (secrets.md §6):
// a part's name, or "init-<name>".
type ConsumerSecrets interface {
	EnvFor(consumer string) map[string]string
}

// IntranetStack is everything the stack file says.
type IntranetStack struct {
	Slug   string
	Hosts  []string
	Domain string
	Image  string // the selected public release image
	// Command is the app's command; empty runs the image's own.
	Command []string
	Port    int
	// Resources is the public part's; each non-public part carries its own.
	Resources appcontract.Resources
	Env       map[string]string // the public part's prepared environment
	Secrets   ConsumerSecrets
	// PublicPart names the public part, the consumer the app service is.
	PublicPart string
	DataDir    string
	Origins    string // APPTOWN_ORIGINS: the tiers a browser reaches the app at
	// Release is the public part's release identity
	// (appcontract.Contract.PublicRelease), APPTOWN_RELEASE, which its file
	// server answers at ReleasePath; "" when the public part is not static.
	Release string
	Needs   []IntranetNeed
	Init    []appcontract.InitContainer
	Parts   []appcontract.Part // non-public app parts only
}

// IntranetCompose renders the stack file. Label conventions follow the
// usual Portainer-with-Traefik stack layout: host-network Traefik
// discovers containers via the docker provider, so no shared network is
// declared; the wildcard cert domain is shared by every app router.
// Ordering is the one main uses: needs come up healthy, init steps run to
// completion, then the app — expressed as compose depends_on conditions so
// the platform enforces it, not a sleep.
func IntranetCompose(st IntranetStack) string {
	var b strings.Builder
	// quoted writes one value from data as a YAML string that Compose
	// reads back unchanged (ComposeLiteral).
	quoted := func(s string) string { return strconv.Quote(ComposeLiteral(s)) }
	b.WriteString("# AppTown-owned stack: deployed by apptownd at release time.\n")
	b.WriteString("# Managed by AppTown, not by your infrastructure-as-code.\n")
	b.WriteString("services:\n")
	var volumes []string

	// writeEnv writes one environment mapping. AppTown's own values
	// (extra: DATA_DIR, APPTOWN_ORIGINS, APPTOWN_RELEASE) are set last into
	// the same map, so each key appears once and theirs wins over a
	// contract's or a secret's of the same name, as the last -e does on a
	// Docker host; Compose refuses a mapping that repeats a key.
	writeEnv := func(env map[string]string, extra ...[2]string) {
		merged := appcontract.MergeEnv(env, nil)
		for _, kv := range extra {
			if kv[1] != "" {
				merged[kv[0]] = kv[1]
			}
		}
		if len(merged) == 0 {
			return
		}
		b.WriteString("    environment:\n")
		keys := make([]string, 0, len(merged))
		for k := range merged {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "      %s: %s\n", k, quoted(merged[k]))
		}
	}
	writeList := func(key string, cmd []string) {
		if len(cmd) == 0 {
			return
		}
		parts := make([]string, len(cmd))
		for i, c := range cmd {
			parts[i] = quoted(c)
		}
		fmt.Fprintf(&b, "    %s: [%s]\n", key, strings.Join(parts, ", "))
	}
	writeCommand := func(cmd []string) { writeList("command", cmd) }
	// writeResources holds a part's service to its declared resources:
	// mem_limit with swap at the same value (the declared figure is swap
	// included) and cpus. Nothing declared writes nothing: no limit.
	writeResources := func(r appcontract.Resources) {
		if r.MemoryBytes > 0 {
			fmt.Fprintf(&b, "    mem_limit: %d\n    memswap_limit: %d\n", r.MemoryBytes, r.MemoryBytes)
		}
		if r.CPUs > 0 {
			fmt.Fprintf(&b, "    cpus: %s\n", strconv.FormatFloat(r.CPUs, 'f', -1, 64))
		}
	}
	writeHealth := func(cmd string) {
		if cmd == "" {
			return
		}
		fmt.Fprintf(&b, "    healthcheck:\n      test: [\"CMD-SHELL\", %s]\n      interval: 3s\n      timeout: 3s\n      retries: 20\n      start_period: 2m\n", quoted(cmd))
	}
	// needsReady: the depends_on block every consumer of the needs shares.
	needsReady := func() []string {
		var out []string
		for _, n := range st.Needs {
			cond := "service_started"
			if n.Health != "" {
				cond = "service_healthy"
			}
			out = append(out, fmt.Sprintf("      %s:\n        condition: %s\n", n.Name, cond))
		}
		return out
	}
	writeDepends := func(deps []string) {
		if len(deps) == 0 {
			return
		}
		b.WriteString("    depends_on:\n")
		for _, d := range deps {
			b.WriteString(d)
		}
	}

	// The app: the public part, behind Traefik.
	b.WriteString("  app:\n")
	fmt.Fprintf(&b, "    image: %s\n", ComposeLiteral(st.Image))
	b.WriteString("    restart: unless-stopped\n")
	writeResources(st.Resources)
	writeCommand(st.Command)
	writeEnv(appcontract.MergeEnv(st.Env, st.Secrets.EnvFor(st.PublicPart)), [2]string{"DATA_DIR", st.DataDir}, [2]string{"APPTOWN_ORIGINS", st.Origins}, [2]string{appcontract.ReleaseEnvVar, st.Release})
	if st.DataDir != "" {
		b.WriteString("    volumes:\n")
		fmt.Fprintf(&b, "      - %s:%s\n", dataVolumeName(st.Slug), ComposeLiteral(st.DataDir))
		volumes = append(volumes, dataVolumeName(st.Slug))
	}
	deps := needsReady()
	for _, it := range st.Init {
		deps = append(deps, fmt.Sprintf("      init-%s:\n        condition: service_completed_successfully\n", it.Name))
	}
	for _, p := range st.Parts {
		cond := "service_started"
		if p.Health != "" {
			cond = "service_healthy"
		}
		deps = append(deps, fmt.Sprintf("      %s:\n        condition: %s\n", p.Name, cond))
	}
	writeDepends(deps)
	rules := make([]string, len(st.Hosts))
	for i, h := range st.Hosts {
		rules[i] = "Host(`" + h + "`)"
	}
	router := lanStackName(st.Slug) // the Traefik router and service share the stack's name
	b.WriteString("    labels:\n")
	b.WriteString("      traefik.enable: \"true\"\n")
	fmt.Fprintf(&b, "      traefik.http.routers.%s.rule: %s\n", router, quoted(strings.Join(rules, " || ")))
	fmt.Fprintf(&b, "      traefik.http.routers.%s.tls: \"true\"\n", router)
	fmt.Fprintf(&b, "      traefik.http.routers.%s.tls.certresolver: \"le\"\n", router)
	fmt.Fprintf(&b, "      traefik.http.routers.%s.tls.domains[0].main: %s\n", router, quoted("*."+st.Domain))
	for i, h := range st.Hosts[1:] {
		fmt.Fprintf(&b, "      traefik.http.routers.%s.tls.domains[%d].main: %s\n", router, i+1, quoted(h))
	}
	fmt.Fprintf(&b, "      traefik.http.services.%s.loadbalancer.server.port: \"%d\"\n", router, st.Port)

	// The needs: emulators with their data in named volumes. Service name =
	// the need's network alias, the same {host} the connection env names.
	for _, n := range st.Needs {
		fmt.Fprintf(&b, "  %s:\n", n.Name)
		fmt.Fprintf(&b, "    image: %s\n", ComposeLiteral(n.Image))
		b.WriteString("    restart: unless-stopped\n")
		writeResources(n.Resources)
		writeList("entrypoint", n.Entrypoint)
		writeCommand(n.Command)
		writeEnv(n.Env)
		writeHealth(n.Health)
		if n.Data != "" {
			vol := lanNeedVolumeName(st.Slug, n.Name)
			b.WriteString("    volumes:\n")
			fmt.Fprintf(&b, "      - %s:%s\n", vol, ComposeLiteral(n.Data))
			volumes = append(volumes, vol)
		}
	}

	// Init steps: run once to completion after the needs are ready; the
	// app waits for them (migrations before the first request).
	for _, it := range st.Init {
		img := it.Image
		fmt.Fprintf(&b, "  init-%s:\n", it.Name)
		fmt.Fprintf(&b, "    image: %s\n", ComposeLiteral(img))
		b.WriteString("    restart: \"no\"\n")
		writeCommand(it.Command)
		writeEnv(appcontract.MergeEnv(it.Env, st.Secrets.EnvFor("init-"+it.Name)))
		writeDepends(needsReady())
	}

	// Non-public parts: on the same stack, reached by name.
	for _, p := range st.Parts {
		img := p.Image
		fmt.Fprintf(&b, "  %s:\n", p.Name)
		fmt.Fprintf(&b, "    image: %s\n", ComposeLiteral(img))
		b.WriteString("    restart: unless-stopped\n")
		writeResources(p.Resources)
		writeCommand(p.Command)
		env := appcontract.MergeEnv(p.Env, st.Secrets.EnvFor(p.Name))
		if p.Port != 0 {
			env["PORT"] = fmt.Sprintf("%d", p.Port)
		}
		writeEnv(env)
		writeHealth(p.Health)
		if p.DataDir != "" {
			vol := dataVolumeName(st.Slug + "-" + p.Name)
			b.WriteString("    volumes:\n")
			fmt.Fprintf(&b, "      - %s:%s\n", vol, ComposeLiteral(p.DataDir))
			volumes = append(volumes, vol)
		}
		writeDepends(needsReady())
	}

	if len(volumes) > 0 {
		b.WriteString("volumes:\n")
		for _, v := range volumes {
			fmt.Fprintf(&b, "  %s: {}\n", v)
		}
	}
	return b.String()
}

// ComposeLiteral is s written so that Compose reads it back unchanged.
// Compose interpolates every value in a compose file: $NAME and ${NAME}
// are replaced from the environment of whoever runs Compose (on Portainer,
// variables AppTown never sets, so they read as empty), and $$ is a
// literal $. AppTown never relies on that: a value it writes (a secret,
// a contract's environment, a start script's shell variables) means what
// it says, as with docker run -e on a Docker host. So each $ is $$.
func ComposeLiteral(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// ComposeRead is what Compose reads from a value AppTown wrote with
// ComposeLiteral: each $$ is one $.
func ComposeRead(s string) string { return strings.ReplaceAll(s, "$$", "$") }

func PreparedReleaseApp(c *appcontract.Plan, image string) (*appcontract.Plan, error) {
	cc := *c
	cc.Parts = append([]appcontract.Part(nil), c.Parts...)
	cc.Init = append([]appcontract.InitContainer(nil), c.Init...)
	for i := range cc.Parts {
		p := &cc.Parts[i]
		if p.Public {
			// A static site's release image is the file server: it still
			// serves /_apptown/env.js and must still be given its values.
			p.ServesStatic = p.IsStatic()
			p.Build, p.Image, p.Static, p.StaticBuild = "", image, "", nil
		} else if p.Build != "" {
			if p.Build != c.PublicPart().Build {
				return nil, fmt.Errorf("release needs a recorded image for separately built part %q", p.Name)
			}
			p.Build, p.Image = "", image
		}
	}
	for i := range cc.Init {
		it := &cc.Init[i]
		if it.Build != "" {
			if it.Build != c.PublicPart().Build {
				return nil, fmt.Errorf("release needs a recorded image for separately built init %q", it.Name)
			}
			it.Build, it.Image = "", image
		}
	}
	return &cc, nil
}

func dataVolumeName(container string) string { return dataVolumePrefix + container }

func lanStackName(slug string) string { return "forge-" + slug }

func lanNeedVolumeName(slug, need string) string { return "forge-need-" + slug + "-" + need }

const dataVolumePrefix = "forge-data-"
