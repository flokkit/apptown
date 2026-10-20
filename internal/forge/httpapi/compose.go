package httpapi

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"

	"gopkg.in/yaml.v3"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/dependency"
)

// generateDevCompose renders the forge-native contract to a docker-compose.yml
// for LOCAL DEVELOPMENT (design/decisions/forge-native-contract.md): every app part plus
// each need's local (emulator) fulfillment, with the connection env wired into
// the app parts. This is the local machine as a fulfillment target — the same
// needs the forge fulfills with emulator containers in previews and terraform
// in cloud, emitted here as a file the dev's own `docker compose up` can run.
//
// It is GENERATED, never authored: `apptown dev` writes it gitignored and rebuilds
// it every time, so it cannot drift from the contract.
func (h Configuration) generateDevCompose(ctx context.Context, c *appcontract.Contract) (string, error) {
	parts := c.Parts
	if len(parts) == 0 {
		return "", fmt.Errorf("app contract declares no parts")
	}
	for _, p := range parts {
		if p.StaticBuild != nil {
			// Compose cannot export a build's files and serve them; the
			// forge builds this form for previews and releases.
			return "", fmt.Errorf("app part %q is a static site with a build, which `apptown dev up` does not run: run its own dev server (for example npm run dev), or apptown preview", p.Name)
		}
	}

	// Fulfill needs locally: emulator services + the env the app reads.
	var err error
	c, err = h.catalog.ResolveSizes(ctx, c)
	if err != nil {
		return "", err
	}
	plan := h.catalog.ResolveLocal(ctx, c.Needs, c.NeedParams, "")
	if len(plan.Unknown) > 0 {
		return "", fmt.Errorf("needs not in the catalog: %v", plan.Unknown)
	}

	services := map[string]any{}
	volumes := map[string]any{}

	// depends_on for the app parts: wait for each need to be healthy (or at
	// least started) and for every init container to complete successfully. This
	// is the ECS dependsOn shape (HEALTHY / SUCCESS), expressed in compose.
	needDep := map[string]any{}
	for _, s := range plan.Services {
		cond := "service_started"
		if s.Health != "" {
			cond = "service_healthy"
		}
		needDep[s.Name] = map[string]any{"condition": cond}
	}
	appDep := map[string]any{}
	for k, v := range needDep {
		appDep[k] = v
	}
	for _, in := range c.Init {
		appDep[in.Name] = map[string]any{"condition": "service_completed_successfully"}
	}

	// Init containers: run-to-completion services (restart: no) that wait for
	// the needs to be healthy, then run once. The app parts depend on them
	// completing.
	for _, in := range c.Init {
		svc := map[string]any{"restart": "no"}
		if in.Image != "" {
			svc["image"] = deploy.ComposeLiteral(in.Image)
		} else {
			svc["build"] = deploy.ComposeLiteral(orDot(in.Build))
		}
		if len(in.Command) > 0 {
			svc["command"] = composeLiteral(in.Command)
		}
		own, err := appcontract.WireEnv(c.Version, "init step "+in.Name, in.Env, plan.Env)
		if err != nil {
			return "", err
		}
		env := appcontract.MergeEnv(own, plan.Env)
		if len(env) > 0 {
			svc["environment"] = composeEnv(env)
		}
		if len(needDep) > 0 {
			svc["depends_on"] = needDep
		}
		services[in.Name] = svc
	}

	for _, p := range parts {
		svc := map[string]any{}
		var mounts []string
		switch {
		case p.Static != "":
			svc["image"] = docker.StaticServerImage
			mounts = []string{"./" + p.Static + ":/srv:ro"}
			svc["command"] = composeLiteral(docker.StaticServerCommand(p.Port))
		case p.Image != "":
			svc["image"] = deploy.ComposeLiteral(p.Image)
			if len(p.Command) > 0 {
				svc["command"] = composeLiteral(p.Command)
			}
		default: // build
			svc["build"] = deploy.ComposeLiteral(orDot(p.Build))
			if len(p.Command) > 0 {
				svc["command"] = composeLiteral(p.Command)
			}
		}
		// Env: the part's own, plus the dependency connection env so the app can
		// reach postgres/minio by service name — identical to what preview injects.
		own, err := appcontract.WireEnv(c.Version, "app part "+p.Name, p.Env, plan.Env)
		if err != nil {
			return "", err
		}
		env := appcontract.MergeEnv(own, plan.Env)
		if p.Port != 0 {
			env["PORT"] = fmt.Sprint(p.Port)
		}
		if len(env) > 0 {
			svc["environment"] = composeEnv(env)
		}
		// The public part is reachable from the host; other parts stay internal.
		if p.Public && p.Port != 0 {
			svc["ports"] = []any{fmt.Sprintf("%d:%d", p.Port, p.Port)}
		}
		if p.DataDir != "" {
			vol := "appdata-" + p.Name
			mounts = append(mounts, vol+":"+p.DataDir)
			volumes[vol] = map[string]any{}
		}
		if len(mounts) > 0 {
			svc["volumes"] = composeLiteral(mounts)
		}
		if hc := composePartHealth(p); hc != nil {
			svc["healthcheck"] = hc
		}
		if len(appDep) > 0 {
			svc["depends_on"] = appDep
		}
		services[p.Name] = svc
	}

	// Need emulators as compose services, with their catalog healthcheck so the
	// app parts can gate on them being ready.
	for _, s := range plan.Services {
		svc := composeSizedNeed(s, c)
		services[s.Name] = svc
	}

	root := map[string]any{"services": services}
	if len(volumes) > 0 {
		root["volumes"] = volumes
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	header := "# GENERATED by `apptown dev` from .apptown/app.yaml — do not edit.\n" +
		"# Regenerate with `apptown dev up`; this file is gitignored.\n"
	return header + string(out), nil
}

// generateNeedsCompose renders ONLY the app's needs (the emulators + their
// healthchecks) — process-mode (design/decisions/forge-native-contract.md): `apptown dev`
// brings these up and the developer/agent runs the app AS PROCESSES against
// them, using the injected connection env. No app parts, no build.
func (h Configuration) generateNeedsCompose(ctx context.Context, c *appcontract.Contract) (string, error) {
	var err error
	c, err = h.catalog.ResolveSizes(ctx, c)
	if err != nil {
		return "", err
	}
	plan := h.catalog.ResolveLocal(ctx, c.Needs, c.NeedParams, "")
	if len(plan.Unknown) > 0 {
		return "", fmt.Errorf("needs not in the catalog: %v", plan.Unknown)
	}
	if len(plan.Services) == 0 {
		return "", fmt.Errorf("this app declares no needs — nothing to bring up in process-mode")
	}
	services := map[string]any{}
	for _, s := range plan.Services {
		svc := composeSizedNeed(s, c)
		// Expose the need's port so a process on the host can reach it directly.
		if s.Port != 0 {
			svc["ports"] = []any{fmt.Sprintf("%d:%d", s.Port, s.Port)}
		}
		services[s.Name] = svc
	}
	out, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return "", err
	}
	header := "# GENERATED by `apptown dev` — the app's NEEDS only (process-mode).\n" +
		"# `apptown dev` brings these up; run your app against the env in .env.\n"
	return header + string(out), nil
}

// generateProcfile renders the app parts as a Procfile (process-mode): one
// `name: command` line per part that has a command, run together by hivemind.
// Returns "" when fewer than two parts have commands — a single process needs
// no Procfile, you just run it. The dev command may differ from the part's
// run/container command, so this is a starting point the agent can edit.
func generateProcfile(c *appcontract.Contract) string {
	var lines []string
	for _, p := range c.Parts {
		if len(p.Command) == 0 {
			continue // relies on the image CMD — no host process command to derive
		}
		lines = append(lines, p.Name+": "+strings.Join(p.Command, " "))
	}
	if len(lines) < 2 {
		return "" // single process: no Procfile, just run it
	}
	return strings.Join(lines, "\n") + "\n"
}

// composeHealth renders a shell healthcheck command as a compose healthcheck
// block (the same shape ECS healthCheck uses: a command + interval/retries).
// composePartHealth renders an app part's health as a compose healthcheck for
// `apptown dev` (local, in-container — the full source image has a shell, so the
// design's platform-executed rule (§4.3) relaxes to an in-container test here).
// The structured intent (design/subsystems/healthchecks.md) wins when declared, else the
// legacy string. Local dev is `direct` — no proxy in front — so nothing is
// front-door shaped; that shaping belongs to the fronted deployments (ingress.go).
func composePartHealth(p appcontract.Part) map[string]any {
	if p.Intent != nil {
		// Readiness is the routing question and the richer signal; prefer it,
		// fall back to liveness.
		// A static part is served by the generated web server on 80 (the
		// same default the docker projection uses); a server part that
		// declared no port listens on 8080 by contract. Probing 8080 for a
		// static site kept a healthy service "starting" forever (complaint #61).
		port := p.Port
		if port == 0 && p.Static != "" {
			port = 80
		}
		for _, pr := range []*appcontract.Probe{p.Intent.Readiness, p.Intent.Liveness} {
			if pr == nil {
				continue
			}
			if cmd := probeShellCmd(pr, port); cmd != "" {
				return composeHealth(cmd)
			}
		}
	}
	if p.Health != "" {
		return composeHealth(p.Health)
	}
	return nil
}

// probeShellCmd renders one probe as an in-container shell command (apptown dev
// only). Any served answer counts as up — the boot-path probe (probeApp)
// enforces the declared status; compose local dev just needs a liveness signal.
func probeShellCmd(pr *appcontract.Probe, partPort int) string {
	switch {
	case pr.HTTP != "":
		return fmt.Sprintf("wget -qO- http://localhost:%d%s >/dev/null 2>&1", portOr(partPort, 8080), pr.HTTP)
	case pr.TCP != 0:
		return fmt.Sprintf("timeout 2 bash -c '</dev/tcp/localhost/%d' 2>/dev/null", pr.TCP)
	case len(pr.Exec) > 0:
		parts := make([]string, len(pr.Exec))
		for i, e := range pr.Exec {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func composeHealth(cmd string) map[string]any {
	return map[string]any{
		"test":         []any{"CMD-SHELL", deploy.ComposeLiteral(cmd)},
		"interval":     "3s",
		"timeout":      "3s",
		"retries":      20,
		"start_period": "2s",
	}
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}
func portOr(p, d int) int {
	if p == 0 {
		return d
	}
	return p
}

// composeNeed renders the emulator shared by container and process development.
func composeNeed(s dependency.LocalService) map[string]any {
	// apptown dev is where the app's own tests run, as in a step.
	s = s.ForSteps()
	svc := map[string]any{"image": deploy.ComposeLiteral(s.Image)}
	if len(s.Entrypoint) > 0 {
		svc["entrypoint"] = composeLiteral(s.Entrypoint)
	}
	if len(s.Command) > 0 {
		svc["command"] = composeLiteral(s.Command)
	}
	if len(s.Env) > 0 {
		svc["environment"] = composeEnv(s.Env)
	}
	if s.Health != "" {
		svc["healthcheck"] = composeHealth(s.Health)
	}
	// A folder where the emulator keeps its data: an anonymous volume, as
	// the forge's own throwaway starts give it. S3's emulator exits at once
	// without /data.
	if s.Data != "" {
		svc["volumes"] = composeLiteral([]string{s.Data})
	}
	return svc
}

func composeSizedNeed(s dependency.LocalService, c *appcontract.Contract) map[string]any {
	if size, ok := c.NeedSizes[s.Name]; ok {
		s.Command = size.Command
	}
	svc := composeNeed(s)
	limits := c.NeedResources[s.Name]
	if limits.MemoryBytes > 0 {
		svc["mem_limit"] = limits.MemoryBytes
		svc["memswap_limit"] = limits.MemoryBytes
	}
	if limits.CPUs > 0 {
		svc["cpus"] = limits.CPUs
	}
	return svc
}

// composeLiteral keeps each value as written (deploy.ComposeLiteral): a
// start script's shell variables reach the container's shell, and a mount
// is at the path the app was given.
func composeLiteral(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = deploy.ComposeLiteral(a)
	}
	return out
}

// composeEnv keeps environment values as written (deploy.ComposeLiteral),
// as docker run -e gives them on a Docker host.
func composeEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = deploy.ComposeLiteral(v)
	}
	return out
}
