package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/trust"
)

const IntranetTargetName = "intranet"

// DefaultPortainerEnvironment is the environment name Portainer gives the
// Docker it runs on when installed with its defaults: the intranet target's
// environment until an administrator changes it on the target.
const DefaultPortainerEnvironment = "local"

// LANReleaseStarted preserves the default release HTTP response contract.
// The accepted release is executed through the durable release queue.
type LANReleaseStarted struct {
	OK     bool         `json:"ok"`
	Number int64        `json:"number"`
	URL    string       `json:"url"`
	State  ReleaseState `json:"state"`
}

// stackImages is what a Portainer stack runs: a published bundle's part
// and init images by digest.
type stackImages struct {
	Parts, Init map[string]string
}

// portainerStackOf is everything a Portainer deployment's stack is made of.
type portainerStackOf struct {
	// Prefix and Base begin the stack's name and its volume keys
	// (portainer.StackNames).
	Base     string
	Prefix   string
	Hosts    []string
	Domain   string
	Contract *appcontract.Contract // the effective contract
	Revision string
	Images   stackImages
	Secrets  deploy.ConsumerSecrets
	// Project is whose attached integrations' values the app is given.
	Project ids.ID
	// Deployment is whose generated need credentials the stack carries.
	Deployment ids.ID
}

// portainerStack renders the admitted Portainer deployment: its needs as
// emulators with persistent volumes, the init steps before the app, the
// other parts beside it, and the front door the app should trust. The
// needs are resolved by the code a Docker host boots from (boot.go); the
// stack renders that, never resolving needs again.
func (e *Environment) portainerStack(ctx context.Context, in portainerStackOf, logf func(string, ...any)) (string, error) {
	// The stack's names are its deployment's: a stack rendered without
	// them would name its volumes anew, away from its data.
	if in.Prefix == "" || in.Base == "" {
		return "", fmt.Errorf("the deployment's names on Portainer: prefix %q, base %q", in.Prefix, in.Base)
	}
	c := in.Contract
	rs, err := e.ResolveDockerStack(ctx, c, nil, e.NeedCredentials(NeedSecretsScope(in.Deployment)))
	if err != nil {
		return "", err
	}
	if err := deploy.RefuseNeedSecretCollisions(rs.Plan, rs.NeedNames, in.Secrets); err != nil {
		return "", err
	}
	plan, err := bundleApp(rs.Plan, in.Images)
	if err != nil {
		return "", err
	}
	given, browser, err := e.integrationValues(ctx, in.Project, c)
	if err != nil {
		return "", err
	}
	origins := make([]string, len(in.Hosts))
	for i, host := range in.Hosts {
		origins[i] = "https://" + host
	}
	st := deploy.IntranetStack{Base: in.Base, Prefix: in.Prefix, Hosts: in.Hosts, Domain: in.Domain, Image: in.Images.Parts[c.PublicPart().Name], Command: plan.PublicPart().Command, Resources: plan.PublicPart().Resources, Port: c.PublicPart().Port,
		Env: plan.PublicPart().Env, Integrations: given, BrowserEnv: browser, DataDir: plan.PublicPart().DataDir, Origins: strings.Join(origins, ","), Init: plan.Init, Needs: docker.IntranetNeeds(rs.Needs),
		Secrets: in.Secrets, PublicPart: c.PublicPart().Name, Release: c.PublicRelease(strings.TrimSpace(in.Revision)),
		// Portainer's machines are not this one: the extra certificates
		// without this machine's local authority.
		Trust: trust.Current().Script(false)}
	if len(given) > 0 {
		logf("giving the app the project's integration values (%s) as stack environment — Portainer keeps a copy with the stack definition", strings.Join(slices.Sorted(maps.Keys(given)), ", "))
	}
	for _, part := range plan.Parts {
		if !part.Public {
			st.Parts = append(st.Parts, part)
		}
	}
	if len(st.Needs) > 0 {
		names := make([]string, len(st.Needs))
		for i, n := range st.Needs {
			names[i] = n.Name
		}
		logf("carrying the app's needs: %s (their data lives in intranet volumes and survives releases)", strings.Join(names, ", "))
		for _, n := range rs.Needs {
			if len(n.Credentials) > 0 {
				logf("the needs' credentials, made for this deployment, go as stack environment — Portainer keeps a copy with the stack definition")
				break
			}
		}
	}
	for _, it := range c.Init {
		logf("init step %q runs to completion before the app starts", it.Name)
	}
	return portainer.MarkCompose(deploy.IntranetCompose(st), in.Deployment)
}

// integrationValues is what a Docker host gives c's public part from the
// project's attached integrations (docker.Host.StartAppContainer): their
// values, and for a static public part its browser values as JSON ("{}"
// for none; "" for a server app, which is not given them). The browser
// values are read strictly: a failed read fails the release, as it fails a
// Docker host's boot.
func (e *Environment) integrationValues(ctx context.Context, projectID ids.ID, c *appcontract.Contract) (map[string]string, string, error) {
	static := c.PublicPart().IsStatic()
	if e.integrations == nil {
		if static {
			return nil, "{}", nil
		}
		return nil, "", nil
	}
	env, err := e.integrations.Environment(ctx, projectID)
	if err != nil {
		return nil, "", fmt.Errorf("reading what the attached integrations give the app: %w", err)
	}
	given := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			given[k] = v
		}
	}
	if !static {
		return given, "", nil
	}
	values, err := e.integrations.BrowserEnvironment(ctx, projectID)
	if err != nil {
		return nil, "", fmt.Errorf("reading the values a browser may be given: %w", err)
	}
	raw, err := json.Marshal(values)
	return given, string(raw), err
}

// bundleApp is plan with every part and init step running its image from a
// published bundle, by digest. A static part's image carries the command
// that serves it (docker.StaticSiteDockerfile); it is still a static site.
func bundleApp(plan *appcontract.Plan, b stackImages) (*appcontract.Plan, error) {
	cc := *plan
	cc.Parts = append([]appcontract.Part(nil), plan.Parts...)
	cc.Init = append([]appcontract.InitContainer(nil), plan.Init...)
	for i := range cc.Parts {
		p := &cc.Parts[i]
		ref := b.Parts[p.Name]
		if ref == "" {
			return nil, fmt.Errorf("the release has no image for part %s", p.Name)
		}
		p.ServesStatic = p.IsStatic()
		p.Image, p.Build, p.Static, p.StaticBuild = ref, "", "", nil
	}
	for i := range cc.Init {
		it := &cc.Init[i]
		ref := b.Init[it.Name]
		if ref == "" {
			return nil, fmt.Errorf("the release has no image for init step %s", it.Name)
		}
		it.Image, it.Build = ref, ""
	}
	return &cc, nil
}
