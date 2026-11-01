package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/trust"
)

// --- the renderer ---------------------------------------------------------------------------

// AppSpec is the App Platform app spec the renderer produces: one
// service per part, a pre-deploy job per init step, the database
// components the needs bound, the health probe from the readiness intent.
// Canonical: keys sorted by the encoder, envs sorted by key, no timestamps.
type AppSpec struct {
	Name      string           `json:"name"`
	Region    string           `json:"region"`
	Services  []Component      `json:"services,omitempty"`
	Workers   []Component      `json:"workers,omitempty"`
	Jobs      []Component      `json:"jobs,omitempty"`
	Databases []map[string]any `json:"databases,omitempty"`
	Ingress   *doIngress       `json:"ingress,omitempty"`
	// Envs are app-wide: the installation's mark (InstanceEnv). App Platform
	// apps take no tags, and the listed spec carries its variables.
	Envs []doEnv `json:"envs,omitempty"`
}

// instanceEnv is the app-wide variable naming the installation that made
// an app: App Platform has no tags, so the spec carries the mark.
const instanceEnv = "APPTOWN_INSTANCE"

// InstallationTag is the DigitalOcean tag naming the installation that
// made a resource (a database cluster).
func InstallationTag(installation string) string { return "apptown-instance:" + installation }

type Component struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind,omitempty"` // jobs: PRE_DEPLOY
	Image            doImage `json:"image"`
	RunCommand       string  `json:"run_command,omitempty"`
	InstanceSizeSlug string  `json:"instance_size_slug"`
	InstanceCount    int     `json:"instance_count,omitempty"`
	HTTPPort         int     `json:"http_port,omitempty"`
	// InternalPorts are a service's ports on the app's private network
	// only: a gated app's public part has these and no http_port, so the
	// platform gives it no route.
	InternalPorts       []int          `json:"internal_ports,omitempty"`
	HealthCheck         *doHealthCheck `json:"health_check,omitempty"`
	LivenessHealthCheck *doHealthCheck `json:"liveness_health_check,omitempty"`
	Envs                []doEnv        `json:"envs,omitempty"`
}

type doImage struct {
	RegistryType string `json:"registry_type"`
	Repository   string `json:"repository"`
	Digest       string `json:"digest,omitempty"`
	Tag          string `json:"tag,omitempty"`
}

type doHealthCheck struct {
	HTTPPath            string `json:"http_path,omitempty"`
	Port                int    `json:"port,omitempty"`
	InitialDelaySeconds int    `json:"initial_delay_seconds,omitempty"`
	PeriodSeconds       int    `json:"period_seconds,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds,omitempty"`
	FailureThreshold    int    `json:"failure_threshold,omitempty"`
	SuccessThreshold    int    `json:"success_threshold,omitempty"`
}

type doEnv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Scope string `json:"scope"`          // RUN_TIME | RUN_AND_BUILD_TIME
	Type  string `json:"type,omitempty"` // GENERAL | SECRET
}

type doIngress struct {
	Rules []doIngressRule `json:"rules"`
}

type doIngressRule struct {
	Match     map[string]any    `json:"match"`
	Component map[string]string `json:"component"`
}

// appURL is App Platform's bindable variable for the app's live URL
// (https:// and its primary domain: a custom domain when one is set, else
// the platform's default ingress), resolved by the platform at run time.
const appURL = "${APP_URL}"

// doAppName is the app's name on the platform: the deployment's key
// (deploy.HostKey), which no other deployment shares. App Platform allows
// 32 characters; this is 20.
func doAppName(key string) string { return "apptown-" + key }

// Render produces the app spec from the release's images. They are put in
// DOCR first, so the spec names digests DOCR holds.
func (d *Driver) Render(ctx context.Context, p deploy.Placement, src deploy.Source, bindings deploy.NeedBindings, secrets deploy.ConsumerSecrets, logf func(string, ...any)) (deploy.Artifact, error) {
	c := src.Contract
	if src.Images == nil || c == nil {
		return deploy.Artifact{}, fmt.Errorf("App Platform renders from built images and their contract")
	}
	selected := appcontract.Prepare(c, nil)
	if p.Gate != nil {
		selected = selected.PublicOnly()
	}
	var needNames []string
	for _, binding := range bindings {
		for name := range binding.Env {
			needNames = append(needNames, name)
		}
		for name := range binding.Secret {
			needNames = append(needNames, name)
		}
	}
	if err := deploy.RefuseNeedSecretCollisions(selected, needNames, secrets); err != nil {
		return deploy.Artifact{}, deploy.Terminal(err)
	}
	// Render writes the spec, so it refuses a value the platform would
	// rewrite whichever caller reached it, before the registry is touched.
	if err := unrewritable(c, secrets); err != nil {
		return deploy.Artifact{}, err
	}
	gate := p.Gate
	if gate != nil {
		if err := gatedRenderable(gate, c); err != nil {
			return deploy.Artifact{}, deploy.Terminal(err)
		}
	}
	question, err := readinessFor(c, gate != nil)
	if err != nil {
		return deploy.Artifact{}, err
	}
	health, err := serviceHealth(c, gate != nil)
	if err != nil {
		return deploy.Artifact{}, err
	}
	client, cfg, err := d.Client(ctx)
	if err != nil {
		return deploy.Artifact{}, err
	}
	// Each part's instance size, before anything is made: a part whose
	// declared resources no size covers, or a size list that cannot be
	// read, stops the release here.
	sizes, err := partSizes(ctx, client.InstanceSizes, cfg.InstanceSize, c)
	if err != nil {
		return deploy.Artifact{}, err
	}
	for _, p := range c.Parts {
		if p.Resources.Declared() {
			logf("part %s declares %s of memory and %s CPUs: App Platform size %s", p.Name, p.Resources.MemoryWords(), p.Resources.CPUWords(), sizes[p.Name])
		}
	}
	registry, err := d.ensureRegistry(ctx, client, cfg, logf)
	if err != nil {
		return deploy.Artifact{}, err
	}
	slug := d.Slug
	// The project's repository is AppTown's: one it made, or one the
	// first copy below makes and AppTown then records. Another's is
	// refused before anything is copied.
	creating, err := d.claimRepository(ctx, client, registry)
	if err != nil {
		return deploy.Artifact{}, err
	}
	// Each distinct image goes to DOCR once; a part and an init step that
	// run the same image share it. A release's image by digest is copied
	// from its registry, keeping its digest; an image on AppTown's own
	// Docker (a release from before releases were published) is pushed.
	pushed := map[string]doImage{}
	imageFor := func(name, local string) (doImage, error) {
		if img, ok := pushed[local]; ok {
			return img, nil
		}
		var digest string
		var err error
		if strings.Contains(local, "@sha256:") {
			digest, err = d.copyImage(ctx, client, registry, slug, local, strs.ShortSHA(src.Revision)+"-"+name, logf)
		} else {
			digest, err = d.options.PushImage(d, ctx, client, registry, slug, local, strs.ShortSHA(src.Revision), logf)
		}
		if err != nil {
			return doImage{}, err
		}
		if creating {
			if err := d.Repository.Created(ctx, registry, slug); err != nil {
				return doImage{}, fmt.Errorf("recording the repository %s AppTown created: %w", slug, err)
			}
			logf("created the image repository %s/%s; it is AppTown's, emptied when the project's last deployment on this account goes", registry, slug)
			creating = false
		}
		img := doImage{RegistryType: "DOCR", Repository: slug, Digest: digest}
		pushed[local] = img
		return img, nil
	}
	spec := AppSpec{Name: doAppName(deploy.HostKey(p.ID)), Region: cfg.Region}
	if d.Installation != "" {
		spec.Envs = []doEnv{{Key: instanceEnv, Value: d.Installation, Scope: "RUN_TIME", Type: "GENERAL"}}
	}
	// Each consumer gets its own environment, resolved need connections,
	// then the secrets its scope names. Public-part values are never global.
	base, needSecrets := map[string]string{}, map[string]string{}
	for _, b := range bindings {
		for k, v := range b.Env {
			base[k] = v
		}
		for k, v := range b.Secret {
			needSecrets[k] = v
		}
	}
	// AppTown's wiring wins over what the app declares, as on every
	// target.
	// Need references in the app's own values (contract version 2) take the
	// need's value as App Platform delivers it (a bindable variable); a value
	// that reads a need credential is sent as a secret, as the credential is.
	needValues := appcontract.MergeEnv(base, needSecrets)
	envsFor := func(part string, own, wiring map[string]string) ([]doEnv, error) {
		wired, err := appcontract.WireEnv(c.Version, part, own, needValues)
		if err != nil {
			return nil, deploy.Terminal(err)
		}
		reads := appcontract.WiresFrom(c.Version, own)
		m := map[string]doEnv{}
		for k, v := range appcontract.MergeEnv(wired, base) {
			// A wired value is sent as a secret: what a need gives is a
			// connection, often with its credential in it (DATABASE_URL,
			// PGPASSWORD), whichever binding map carries it.
			kind := "GENERAL"
			if len(reads[k]) > 0 {
				kind = "SECRET"
			}
			m[k] = doEnv{Key: k, Value: v, Scope: "RUN_TIME", Type: kind}
		}
		for k, v := range secrets.EnvFor(part) {
			m[k] = doEnv{Key: k, Value: v, Scope: "RUN_TIME", Type: "SECRET"}
		}
		// A need's credentials are the platform's secrets, and AppTown's
		// wiring, as its connection is.
		for k, v := range needSecrets {
			m[k] = doEnv{Key: k, Value: v, Scope: "RUN_TIME", Type: "SECRET"}
		}
		for k, v := range wiring {
			m[k] = doEnv{Key: k, Value: v, Scope: "RUN_TIME", Type: "GENERAL"}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]doEnv, 0, len(keys))
		for _, k := range keys {
			out = append(out, m[k])
		}
		return out, nil
	}
	var public string
	publicPort := 0
	for _, p := range c.Parts {
		// Behind the gate only the public part runs: workers and
		// schedulers, which send mail and call webhooks, stay off.
		if gate != nil && !p.Public {
			continue
		}
		local := src.Images[p.Name]
		if local == "" {
			return deploy.Artifact{}, deploy.Terminal(fmt.Errorf("the bundle has no image for part %s", p.Name))
		}
		img, err := imageFor(p.Name, local)
		if err != nil {
			return deploy.Artifact{}, err
		}
		// The public part is told where a browser reaches it, as a Docker
		// deployment's is (APPTOWN_ORIGINS), so its framework trusts its front
		// door: the platform's own URL for the app, its primary domain,
		// which App Platform binds when the app runs.
		wiring := map[string]string{}
		if p.Public {
			wiring["APPTOWN_ORIGINS"] = appURL
		}
		// A static part's image answers its release identity at
		// ReleasePath: the front door is witnessed serving this part of
		// this release (expectedRelease).
		if p.IsStatic() && src.Revision != "" {
			wiring[appcontract.ReleaseEnvVar] = appcontract.ReleaseIdentity(p.Name, src.Revision)
		}
		// The extra certificates the app's own command runs (internal/trust);
		// a static part's file server calls nothing.
		if script := trust.Current().Script(false); script != "" && !p.IsStatic() {
			wiring[appcontract.TrustEnvVar] = script
		}
		envs, err := envsFor(p.Name, p.Env, wiring)
		if err != nil {
			return deploy.Artifact{}, err
		}
		comp := Component{Name: p.Name, Image: img, InstanceSizeSlug: sizes[p.Name], InstanceCount: 1,
			RunCommand: strings.Join(p.Command, " "), Envs: envs, LivenessHealthCheck: health[p.Name].liveness}
		if p.Port > 0 {
			comp.HTTPPort = p.Port
			comp.HealthCheck = health[p.Name].readiness
			if p.Public {
				public, publicPort = p.Name, p.Port
				if gate != nil {
					// Private: the gate is the only way in. The health
					// check runs on the private port, which the platform
					// requires to be listed here.
					comp.HTTPPort, comp.InternalPorts = 0, []int{p.Port}
				}
			}
			spec.Services = append(spec.Services, comp)
		} else {
			spec.Workers = append(spec.Workers, comp)
		}
	}
	if public == "" {
		return deploy.Artifact{}, deploy.Terminal(errors.New("the contract has no public part with a port — App Platform needs a front door"))
	}
	for _, s := range c.Init {
		local := src.Init[s.Name]
		if local == "" {
			return deploy.Artifact{}, deploy.Terminal(fmt.Errorf("the bundle has no image for init step %s", s.Name))
		}
		img, err := imageFor("init-"+s.Name, local)
		if err != nil {
			return deploy.Artifact{}, err
		}
		var wiring map[string]string
		if script := trust.Current().Script(false); script != "" {
			wiring = map[string]string{appcontract.TrustEnvVar: script}
		}
		envs, err := envsFor("init-"+s.Name, s.Env, wiring)
		if err != nil {
			return deploy.Artifact{}, err
		}
		spec.Jobs = append(spec.Jobs, Component{Name: s.Name, Kind: "PRE_DEPLOY", Image: img, InstanceSizeSlug: cfg.InstanceSize,
			RunCommand: strings.Join(s.Command, " "), Envs: envs})
	}
	needs := make([]string, 0, len(bindings))
	for n := range bindings {
		needs = append(needs, n)
	}
	sort.Strings(needs)
	for _, n := range needs {
		if bindings[n].Native != nil {
			spec.Databases = append(spec.Databases, bindings[n].Native)
		}
	}
	front := public
	if gate != nil {
		img, err := d.copyGateImage(ctx, client, registry, gate.Image, logf)
		if err != nil {
			return deploy.Artifact{}, err
		}
		front = GateComponent
		spec.Services = append(spec.Services, gateComponent(img, cfg.InstanceSize, gate, public, publicPort))
	}
	spec.Ingress = &doIngress{Rules: []doIngressRule{{Match: map[string]any{"path": map[string]any{"prefix": "/"}}, Component: map[string]string{"name": front}}}}
	if err := oneVersionPerRepository(spec); err != nil {
		return deploy.Artifact{}, err
	}
	return encodeAppArtifact(spec, question)
}

func blankSecrets(spec AppSpec) string {
	cp := spec
	blank := func(cs []Component) []Component {
		out := make([]Component, len(cs))
		for i, c := range cs {
			c.Envs = append([]doEnv(nil), c.Envs...)
			for j := range c.Envs {
				if c.Envs[j].Type == "SECRET" {
					c.Envs[j].Value = "<secret>"
				}
			}
			out[i] = c
		}
		return out
	}
	cp.Services, cp.Workers, cp.Jobs = blank(spec.Services), blank(spec.Workers), blank(spec.Jobs)
	b, _ := json.Marshal(cp)
	return string(b)
}

// serviceHealth is shared by preflight and rendering: unsupported declarations
// must be refused before need provisioning as well as image publication.
type renderedHealth struct{ readiness, liveness *doHealthCheck }

func serviceHealth(c *appcontract.Contract, gated bool) (map[string]renderedHealth, error) {
	health := make(map[string]renderedHealth)
	if c == nil {
		return health, nil
	}
	for _, part := range c.Parts {
		if gated && !part.Public {
			continue
		}
		var intent appcontract.Health
		if part.Intent != nil {
			intent = *part.Intent
		}
		if part.Port <= 0 {
			if intent.Readiness != nil || intent.Liveness != nil {
				return nil, deploy.Terminal(fmt.Errorf("part %s: App Platform HTTP health requires a declared service port; worker health is not supported by this probe", part.Name))
			}
			continue
		}
		ready, err := renderProbe(&part, intent.Readiness, "readiness")
		if err != nil {
			return nil, deploy.Terminal(fmt.Errorf("part %s readiness: %w", part.Name, err))
		}
		var live *doHealthCheck
		if intent.Liveness != nil {
			live, err = renderProbe(&part, intent.Liveness, "liveness")
			if err != nil {
				return nil, deploy.Terminal(fmt.Errorf("part %s liveness: %w", part.Name, err))
			}
		}
		health[part.Name] = renderedHealth{ready, live}
	}
	return health, nil
}

func readinessDefaults() appcontract.HealthPolicy {
	return appcontract.HealthPolicy{Period: 10 * time.Second, Timeout: 5 * time.Second, StartupGrace: 5 * time.Second, FailureThreshold: 6, SuccessThreshold: 1}
}

func renderProbe(part *appcontract.Part, probe *appcontract.Probe, purpose string) (*doHealthCheck, error) {
	defaults := readinessDefaults()
	if purpose == "liveness" {
		defaults.Timeout = time.Second
		defaults.FailureThreshold = 18
	}
	policy, err := appcontract.NormalizeHealthPolicy(probe, purpose, defaults)
	if err != nil {
		return nil, err
	}
	if probe != nil && (probe.HTTP == "" || probe.TCP != 0 || len(probe.Exec) > 0) {
		return nil, errors.New("App Platform supports declared HTTP health here; TCP and exec require another supported capability")
	}
	if purpose == "liveness" && (policy.SuccessThreshold != 1 || (probe != nil && probe.Expect != 0)) {
		return nil, errors.New("App Platform liveness requires successThreshold 1 and cannot enforce an exact HTTP status")
	}
	if purpose == "readiness" && part.Public {
		if err := policy.CheckBootFit(); err != nil {
			return nil, err
		}
	}
	if policy.StartupGrace > time.Hour {
		return nil, errors.New("startupGrace exceeds App Platform's 3600-second limit")
	}
	seconds := policy.StartupGrace / time.Second
	if policy.StartupGrace%time.Second != 0 {
		seconds++
	}
	hc := &doHealthCheck{Port: part.Port, InitialDelaySeconds: int(seconds), PeriodSeconds: int(policy.Period / time.Second), TimeoutSeconds: int(policy.Timeout / time.Second), FailureThreshold: policy.FailureThreshold}
	if policy.Explicit {
		hc.SuccessThreshold = policy.SuccessThreshold
	}
	if probe != nil {
		hc.HTTPPath = probe.HTTP
	}
	return hc, nil
}

// GateComponent is AppTown gate's component in a gated app.
const GateComponent = "apptown-gate"

// gatedRenderable refuses a gated placement the renderer cannot put behind
// the gate whole: no secret (the gate would let nobody in, and AppTown
// could not check it), an image not pinned by digest, no AppTown to redeem
// tickets at, or a part already named as the gate.
func gatedRenderable(g *deploy.Gate, c *appcontract.Contract) error {
	switch {
	case g.Secret == "":
		return errors.New("a gated app is rendered only while AppTown starts it, with its gate secret")
	case !strings.Contains(g.Image, "@sha256:"):
		return fmt.Errorf("the gate image %q is not pinned by digest", g.Image)
	case g.AppTown == "":
		return errors.New("the gate needs AppTown's public address to redeem tickets at")
	case len(g.Marker) < 32:
		return errors.New("a gated app is rendered only with the random mark AppTown recorded for it")
	}
	for _, p := range c.Parts {
		if p.Name == GateComponent {
			return fmt.Errorf("the part %s has the gate's name", p.Name)
		}
	}
	return nil
}

// gateComponent is the gate: the app's only public component and only
// ingress. Its secret is the provider's encrypted secret, given to it
// alone; its health check is TCP only, so no anonymous request ever
// reaches the app behind it.
func gateComponent(img doImage, size string, g *deploy.Gate, upstream string, port int) Component {
	c := Component{Name: GateComponent, Image: img, InstanceSizeSlug: size, InstanceCount: 1, HTTPPort: deploy.GatePort,
		HealthCheck: &doHealthCheck{Port: deploy.GatePort, InitialDelaySeconds: 2, PeriodSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 6},
		Envs: []doEnv{
			{Key: deploy.GateEnvAppTown, Value: g.AppTown, Scope: "RUN_TIME", Type: "GENERAL"},
			{Key: deploy.GateEnvListen, Value: fmt.Sprintf(":%d", deploy.GatePort), Scope: "RUN_TIME", Type: "GENERAL"},
			{Key: deploy.GateEnvMarker, Value: g.Marker, Scope: "RUN_TIME", Type: "GENERAL"},
			{Key: deploy.GateEnvSecret, Value: g.Secret, Scope: "RUN_TIME", Type: "SECRET"},
			{Key: deploy.GateEnvUpstream, Value: fmt.Sprintf("http://%s:%d", upstream, port), Scope: "RUN_TIME", Type: "GENERAL"},
		}}
	// The gate redeems tickets with AppTown over HTTPS: it reads the extra
	// certificates out of the same script (internal/gate).
	if script := trust.Current().Script(false); script != "" {
		c.Envs = append(c.Envs, doEnv{Key: trust.EnvVar, Value: script, Scope: "RUN_TIME", Type: "GENERAL"})
	}
	return c
}
