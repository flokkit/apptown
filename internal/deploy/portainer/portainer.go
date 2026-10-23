// Package portainer is the Portainer target driver (design/subsystems/
// deployment.md §8.2): the intranet rail given its name. The stack is the
// handle; Portainer owns liveness; AppTown asks it to run a generation
// and records what it answered. No *App, no database handle: the target's
// config is resolved by the caller at use time.
package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/strs"
)

// Target is a Portainer environment as AppTown reaches it: the API and
// its key resolved at use time, the endpoint's name, the apps domain, and
// the tier name release rows carry.
type Target struct {
	PortainerURL      string
	PortainerKey      string
	AppsDomain        string
	EnvName           string
	ReleaseDeployment string // the tier name on release rows (default "lan")
}

// ---- the Portainer API (the deploy surface the platform registered for us) ----

func JSON(ctx context.Context, cfg *Target, method, path string, body any) (int, []byte, error) {
	var rdr *strings.Reader
	if body != nil {
		bb, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encoding Portainer request: %w", err)
		}
		rdr = strings.NewReader(string(bb))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.PortainerURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-Key", cfg.PortainerKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading Portainer response: %w", err)
	}
	if len(raw) > 1<<20 {
		return resp.StatusCode, nil, errors.New("Portainer response exceeds 1 MiB")
	}
	return resp.StatusCode, raw, nil
}

// EndpointPlatform asks the endpoint's Docker what it runs on, in OCI form
// ("linux/amd64"). An endpoint that does not answer, or names no known
// platform, is an error: never a guess, and never "any platform".
func EndpointPlatform(ctx context.Context, cfg *Target, endpointID int64) (string, error) {
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/version", endpointID), nil)
	if err != nil {
		return "", fmt.Errorf("asking Portainer's endpoint %d for its platform: %w", endpointID, err)
	}
	if code != 200 {
		return "", fmt.Errorf("asking Portainer's endpoint %d for its platform: HTTP %d", endpointID, code)
	}
	var v struct{ Os, Arch string }
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("Portainer's endpoint %d answered no platform: %w", endpointID, err)
	}
	platform, err := oci.Platform(v.Os, v.Arch)
	if err != nil {
		return "", fmt.Errorf("Portainer's endpoint %d names no known platform: %w", endpointID, err)
	}
	return platform, nil
}

// PlatformText is the endpoint's platform for a person to read: the
// platform, or why it is unknown.
func PlatformText(ctx context.Context, cfg *Target, endpointID int64) string {
	platform, err := EndpointPlatform(ctx, cfg, endpointID)
	if err != nil {
		return "platform unknown: " + strs.Truncate(err.Error(), 120)
	}
	return platform
}

// endpointsPath lists Portainer's environments without their Docker
// snapshots: with them, the answer for a busy environment is megabytes
// (each container and image), past what AppTown reads.
const endpointsPath = "/api/endpoints?excludeSnapshots=true"

func EndpointID(ctx context.Context, cfg *Target) (int64, error) {
	code, raw, err := JSON(ctx, cfg, "GET", endpointsPath, nil)
	if err != nil {
		return 0, fmt.Errorf("listing Portainer endpoints: %w", err)
	}
	if code != 200 {
		return 0, fmt.Errorf("listing Portainer endpoints: HTTP %d", code)
	}
	var eps []struct {
		ID   int64  `json:"Id"`
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(raw, &eps); err != nil {
		return 0, err
	}
	for _, e := range eps {
		if e.Name == cfg.EnvName {
			return e.ID, nil
		}
	}
	return 0, fmt.Errorf("Portainer has no environment named %q — is the platform's forge-env applied?", cfg.EnvName)
}

// Stack is one of Portainer's stacks: its ID, name and endpoint.
type Stack struct {
	ID         int64  `json:"Id"`
	Name       string `json:"Name"`
	EndpointID int64  `json:"EndpointId"`
}

// ListStacks is every stack Portainer holds.
func ListStacks(ctx context.Context, cfg *Target) ([]Stack, error) {
	code, raw, err := JSON(ctx, cfg, "GET", "/api/stacks", nil)
	if err != nil {
		return nil, fmt.Errorf("listing stacks: %w", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("listing stacks: HTTP %d", code)
	}
	var stacks []Stack
	if err := json.Unmarshal(raw, &stacks); err != nil {
		return nil, fmt.Errorf("decoding Portainer stacks: %w", err)
	}
	return stacks, nil
}

// StackApplied says what DeployStack did: the stack's ID (0 when Portainer
// did not say it), and Created when this call made the stack.
type StackApplied struct {
	Created bool
	ID      int64
}

// DeployStack updates the endpoint's stack of that name, or creates it.
func DeployStack(ctx context.Context, cfg *Target, endpointID int64, name, compose string) (StackApplied, error) {
	var applied StackApplied
	err := deployStack(ctx, cfg, endpointID, name, compose, &applied)
	return applied, err
}

func deployStack(ctx context.Context, cfg *Target, endpointID int64, name, compose string, applied *StackApplied) error {
	code, raw, err := JSON(ctx, cfg, "GET", "/api/stacks", nil)
	if err != nil {
		return fmt.Errorf("listing stacks: %w", err)
	}
	if code != 200 {
		return fmt.Errorf("listing stacks: HTTP %d", code)
	}
	var stacks []struct {
		ID         int64  `json:"Id"`
		Name       string `json:"Name"`
		EndpointID int64  `json:"EndpointId"`
	}
	if err := json.Unmarshal(raw, &stacks); err != nil {
		return fmt.Errorf("decoding Portainer stacks: %w", err)
	}
	if stacks == nil {
		return errors.New("Portainer stack list is null")
	}
	for _, st := range stacks {
		if st.ID <= 0 || st.Name == "" || st.EndpointID <= 0 {
			return errors.New("Portainer stack list contains an incomplete identity")
		}
		if st.Name == name && st.EndpointID == endpointID {
			code, raw, err = JSON(ctx, cfg, "PUT",
				fmt.Sprintf("/api/stacks/%d?endpointId=%d", st.ID, endpointID),
				map[string]any{"stackFileContent": compose, "pullImage": true, "prune": true})
			if err != nil {
				return fmt.Errorf("updating stack %s: %w", name, err)
			}
			if code >= 300 {
				return fmt.Errorf("updating stack %s: HTTP %d %s", name, code, strs.Truncate(string(raw), 300))
			}
			applied.ID = st.ID
			return nil
		}
	}
	code, raw, err = JSON(ctx, cfg, "POST",
		fmt.Sprintf("/api/stacks/create/standalone/string?endpointId=%d", endpointID),
		map[string]any{"name": name, "stackFileContent": compose})
	if err == nil && (code == 404 || code == 405) {
		// Older Portainer route shape.
		code, raw, err = JSON(ctx, cfg, "POST",
			fmt.Sprintf("/api/stacks?type=2&method=string&endpointId=%d", endpointID),
			map[string]any{"name": name, "stackFileContent": compose})
	}
	if err != nil {
		return fmt.Errorf("creating stack %s: %w", name, err)
	}
	if code >= 300 {
		return fmt.Errorf("creating stack %s: HTTP %d %s", name, code, strs.Truncate(string(raw), 300))
	}
	var made struct {
		ID int64 `json:"Id"`
	}
	_ = json.Unmarshal(raw, &made)
	applied.Created, applied.ID = true, made.ID
	return nil
}

// --- the Portainer target ------------------------------------------------------------------

// Driver is the intranet rail given its name (§8.2): the stack
// is the handle; Portainer owns liveness. The intranet release keeps its
// own flow (runLANRelease) and records through the deployment.
type Driver struct {
	TargetID ids.ID // AppTown's target record
	Config   func(targetID ids.ID) (*Target, error)
	// Owner records the stack when AppTown creates it; nil records
	// nothing, and then take-down deletes no stack.
	Owner deploy.Owner
}

func (d *Driver) Resume(ctx context.Context, p deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (deploy.Observation, error) {
	// A lost apply response does not prove whether this generation reached
	// Portainer. Until the adapter can verify it, leave the attempt unresolved.
	return deploy.Observation{}, errors.New("AppTown restarted while Portainer was applying the stack; its generation cannot be verified automatically, so inspect the stack before resolving the attempt")
}

func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	cfg, err := d.Config(d.TargetID)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	if art.Kind != "compose" {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("the Portainer driver deploys compose artifacts, not %s", art.Kind))
	}
	endpointID, err := EndpointID(ctx, cfg)
	if err != nil {
		return deploy.Observation{}, err
	}
	// A deployment not yet released has no handle: its stack is named as
	// the intranet release names it (StackNames).
	stack, _ := p.Handle["stack"].(string)
	prefix, base := StackNames(p.ID)
	if stack == "" && !p.ID.IsZero() {
		stack = StackName(prefix, base)
	}
	if stack == "" {
		return deploy.Observation{}, deploy.Terminal(errors.New("the deployment's handle names no stack"))
	}
	return (Execution{Config: *cfg, EndpointID: endpointID, Stack: stack, Prefix: prefix, Compose: string(art.Body),
		URL: p.URL, Handle: p.Handle, Log: logf, Owner: d.Owner}).Execute(ctx, generation, checkpoint)
}

// Observe asks Portainer whether the deployment's stack's app container
// runs (observeStacks).
func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	cfg, err := d.Config(d.TargetID)
	if err != nil {
		return deploy.Observation{}, err
	}
	out, err := observeStacks(ctx, cfg, []deploy.Placement{p})
	if err != nil {
		return deploy.Observation{}, err
	}
	return out[0], nil
}

// appLabels are the Compose labels on a stack's app container: Portainer
// runs each stack as the Compose project of its name, and AppTown's stacks
// name the app's service "app" (deploy.IntranetCompose).
const (
	composeProject = "com.docker.compose.project"
	composeService = "com.docker.compose.service"
)

// observeStacks says, for each placement, whether its stack's app container
// runs, from one list of the environment's app containers through
// Portainer's Docker proxy. Running is "active"; a container that exists
// and does not run, or none at all, is "absent", as on a Docker host. A
// placement whose handle names no stack is unknown, unless nothing was ever
// released there. A failed look is an error for every placement: it is
// never read as stopped. It reports the recorded generation: Portainer's
// stacks carry no AppTown identity, so this says whether the app runs, and
// (Running) the image its app container was made from, not which release.
// A stopped app container still names its image; a stack with none names
// nothing.
func observeStacks(ctx context.Context, cfg *Target, ps []deploy.Placement) ([]deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]deploy.Observation, len(ps))
	var asked bool
	for i, p := range ps {
		out[i] = deploy.Observation{Generation: p.Generation, Rollout: "unknown", Health: "unknown", URL: p.URL}
		if stack, _ := p.Handle["stack"].(string); stack != "" {
			asked = true
		} else if p.Generation == 0 && p.URL == "" {
			out[i] = deploy.Observation{Rollout: "absent", Health: "unknown"}
		}
	}
	if !asked {
		return out, nil
	}
	endpointID, err := EndpointID(ctx, cfg)
	if err != nil {
		return nil, err
	}
	apps, err := appContainers(ctx, cfg, endpointID)
	if err != nil {
		return nil, err
	}
	for i, p := range ps {
		stack, _ := p.Handle["stack"].(string)
		if stack == "" {
			continue
		}
		app := apps[strings.ToLower(stack)]
		if app.running {
			out[i].Rollout, out[i].Health = "active", "healthy"
		} else {
			out[i].Rollout, out[i].Health = "absent", "unhealthy"
		}
		out[i].Running = app.image
	}
	return out, nil
}

// stackApp is what a Compose project's app containers say: whether one
// runs, and the image of the running one (or of a stopped one when none
// runs).
type stackApp struct {
	running bool
	image   string
}

// appContainers lists the environment's app containers, stopped ones too,
// and says per Compose project (lower case) whether one of them runs and
// from which image. A project with only stopped app containers does not
// run; one with none is missing, and names no image.
func appContainers(ctx context.Context, cfg *Target, endpointID int64) (map[string]stackApp, error) {
	filters := url.QueryEscape(`{"label":["` + composeService + `=app"]}`)
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/json?all=1&filters=%s", endpointID, filters), nil)
	if err != nil {
		return nil, fmt.Errorf("listing the app containers in Portainer's environment %q: %w", cfg.EnvName, err)
	}
	if code != 200 {
		return nil, fmt.Errorf("listing the app containers in Portainer's environment %q: HTTP %d", cfg.EnvName, code)
	}
	var containers []struct {
		State  string            `json:"State"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(raw, &containers); err != nil {
		return nil, fmt.Errorf("reading the app containers in Portainer's environment %q: %w", cfg.EnvName, err)
	}
	if containers == nil {
		return nil, fmt.Errorf("Portainer's environment %q answered no container list", cfg.EnvName)
	}
	apps := map[string]stackApp{}
	for _, c := range containers {
		if c.Labels[composeService] != "app" || c.Labels[composeProject] == "" {
			continue
		}
		project := strings.ToLower(c.Labels[composeProject])
		app, runs := apps[project], c.State == "running"
		if runs || (!app.running && app.image == "") {
			app.image = c.Image
		}
		app.running = app.running || runs
		apps[project] = app
	}
	return apps, nil
}

// Teardown deletes the stack AppTown recorded creating for the
// deployment, and then each volume on the deployment's list (one the
// AppTown's deploy made) when its data may go: a complete final backup
// covers it, or the data is synthetic. Every other volume the stack's file
// declares, and a listed one whose data may not go, stays, reported Left
// for a person. A stack AppTown did not record creating is its Portainer
// owner's to take down, and the teardown refuses.
func (d *Driver) Teardown(ctx context.Context, p deploy.Placement, generation int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	var out deploy.TakenDown
	if p.Generation != generation {
		return out, fmt.Errorf("deployment is at generation %d, not %d — refusing to tear down what was not recorded", p.Generation, generation)
	}
	var stacks []deploy.Owned
	for _, o := range td.Owned {
		if o.Kind == "stack" {
			stacks = append(stacks, o)
		}
	}
	if len(stacks) == 0 {
		return out, errors.New("AppTown did not record creating this deployment's stack, so taking it down is done in Portainer by its owner")
	}
	cfg, err := d.Config(d.TargetID)
	if err != nil {
		return out, err
	}
	endpointID, err := EndpointID(ctx, cfg)
	if err != nil {
		return out, err
	}
	listed := map[string]bool{}
	for _, o := range td.Owned {
		if o.Kind == "volume" {
			listed[o.ID] = true
		}
	}
	for _, st := range stacks {
		vols, verr := stackVolumes(ctx, cfg, st)
		for name := range listed {
			if !slices.Contains(vols, name) {
				vols = append(vols, name) // a volume an earlier release made
			}
		}
		code, raw, err := JSON(ctx, cfg, "DELETE", fmt.Sprintf("/api/stacks/%s?endpointId=%d", url.PathEscape(st.ID), endpointID), nil)
		if err != nil {
			return out, fmt.Errorf("deleting stack %s: %w", st.Name, err)
		}
		if code >= 300 && code != 404 {
			return out, fmt.Errorf("deleting stack %s: HTTP %d %s", st.Name, code, strs.Truncate(string(raw), 300))
		}
		if code != 404 {
			out.Deleted = append(out.Deleted, st.Label())
		}
		for _, v := range vols {
			why := "kept: not made by AppTown's deploy, so not the deployment's to delete; delete it in Portainer, then mark it deleted"
			if listed[v] {
				if td.BackedUp || td.Synthetic {
					if err := deleteVolume(ctx, cfg, endpointID, v); err != nil {
						why = "kept: deleting it failed (" + err.Error() + "); delete it in Portainer, then mark it deleted"
					} else {
						out.Deleted = append(out.Deleted, "volume "+v)
						continue
					}
				} else {
					why = "kept: no complete final backup covers it; delete it in Portainer, then mark it deleted"
				}
			}
			out.Left = append(out.Left, deploy.Left{What: "volume " + v, Data: true, Handle: map[string]any{"kind": "volume", "name": v, "stack": st.Name}, Why: why})
		}
		if verr != nil {
			out.Left = append(out.Left, deploy.Left{What: "the volumes of stack " + st.Name, Data: true, Handle: map[string]any{"kind": "volume", "stack": st.Name},
				Why: "kept, and not read before the stack went (" + verr.Error() + "); look for volumes named " + st.Name + "_… in Portainer"})
		}
	}
	return out, nil
}

// StackFile is a stack's compose file, as Portainer holds it.
func StackFile(ctx context.Context, cfg *Target, stackID string) (string, error) {
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/stacks/%s/file", url.PathEscape(stackID)), nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("HTTP %d", code)
	}
	var file struct {
		Content string `json:"StackFileContent"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return "", err
	}
	return file.Content, nil
}

// stackVolumes names the volumes a stack's compose file declares, as the
// endpoint holds them (<stack>_<name>, or the name it sets).
func stackVolumes(ctx context.Context, cfg *Target, st deploy.Owned) ([]string, error) {
	content, err := StackFile(ctx, cfg, st.ID)
	if err != nil {
		return nil, err
	}
	file := struct{ Content string }{content}
	var compose struct {
		Volumes map[string]*struct {
			Name     string `yaml:"name"`
			External any    `yaml:"external"`
		} `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(file.Content), &compose); err != nil {
		return nil, err
	}
	var out []string
	for key, v := range compose.Volumes {
		switch {
		case v != nil && v.External != nil && v.External != false:
			continue // not the stack's
		case v != nil && v.Name != "":
			out = append(out, v.Name)
		default:
			out = append(out, st.Name+"_"+key)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *Driver) Controls() deploy.Controls {
	return deploy.Controls{Liveness: true, Rollout: true}
}

// Both bundle and intranet releases execute this prepared request. Config is
// execution-only; checkpoints contain provider identity, never credentials.
type Execution struct {
	Config              Target
	EndpointID          int64
	Stack, Compose, URL string
	// Prefix is what the stack's names begin with (the deployment's
	// recorded host prefix): the stack is <prefix><slug>.
	Prefix string
	Handle map[string]any
	Log    func(string, ...any)
	// Owner records the stack when this execution creates it; nil records
	// nothing.
	Owner deploy.Owner
}

func (p Execution) Execute(ctx context.Context, generation int64, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	if p.Stack == "" || p.EndpointID <= 0 {
		return deploy.Observation{}, deploy.Terminal(errors.New("Portainer execution requires a stack and endpoint"))
	}
	if err := checkpoint(map[string]any{"stack": p.Stack, "endpoint": p.Config.EnvName, "endpointId": p.EndpointID}); err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("recording Portainer identity: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	// The volumes the compose declares, and which the environment holds
	// before the deploy: one there before is not the deployment's to delete.
	vols, verr := ComposeVolumes(p.Stack, strings.TrimPrefix(p.Stack, p.Prefix), p.Compose)
	before := map[string]bool{}
	if p.Owner != nil && verr == nil {
		for _, v := range vols {
			there, err := volumeThere(ctx, &p.Config, p.EndpointID, v.Name)
			// Not known is not absent: it is never recorded as made.
			before[v.Name] = there || err != nil
		}
	}
	p.Log("asking Portainer to run generation %d (stack %s on %s)", generation, p.Stack, p.Config.EnvName)
	applied, err := DeployStack(ctx, &p.Config, p.EndpointID, p.Stack, p.Compose)
	if err != nil {
		return deploy.Observation{}, err
	}
	if applied.Created && p.Owner != nil {
		if applied.ID <= 0 {
			// Not recorded, so never deleted by AppTown: a safe miss.
			p.Log("Portainer created the stack %s without saying its ID; it is not recorded as AppTown's, so taking it down is done in Portainer", p.Stack)
		} else if err := p.Owner.Created(ctx, deploy.Owned{Kind: "stack", ID: strconv.FormatInt(applied.ID, 10), Name: p.Stack}); err != nil {
			return deploy.Observation{}, fmt.Errorf("recording the stack %s AppTown created: %w", p.Stack, err)
		}
	}
	if p.Owner != nil && verr == nil {
		if err := recordMadeVolumes(ctx, &p.Config, p.EndpointID, before, vols, p.Owner, p.Log); err != nil {
			return deploy.Observation{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	handle := make(map[string]any, len(p.Handle)+2)
	for key, value := range p.Handle {
		handle[key] = value
	}
	handle["stack"], handle["endpoint"] = p.Stack, p.Config.EnvName
	return deploy.Observation{Generation: generation, Rollout: "active", Health: "unknown", URL: p.URL, Handle: handle}, nil
}

// Inventory answers what the Portainer endpoint holds: the stacks it runs,
// as Portainer reports them for the target's endpoint. Built for a target,
// it can look, never deploy.
type Inventory struct {
	Config func() (*Target, error)
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	cfg, err := i.Config()
	if err != nil {
		return nil, err
	}
	endpointID, err := EndpointID(ctx, cfg)
	if err != nil {
		return nil, err
	}
	code, raw, err := JSON(ctx, cfg, "GET", "/api/stacks", nil)
	if err != nil || code != 200 {
		return nil, fmt.Errorf("listing stacks: HTTP %d %w", code, err)
	}
	var stacks []struct {
		ID         int64  `json:"Id"`
		Name       string `json:"Name"`
		EndpointID int64  `json:"EndpointId"`
		Status     int    `json:"Status"`
	}
	if err := json.Unmarshal(raw, &stacks); err != nil {
		return nil, err
	}
	var res []deploy.Resource
	for _, st := range stacks {
		if st.EndpointID != endpointID {
			continue
		}
		status := "active"
		if st.Status == 2 {
			status = "inactive"
		}
		res = append(res, deploy.Resource{Kind: "stack", Name: st.Name, ID: fmt.Sprint(st.ID), Status: status})
	}
	return res, nil
}

// ObserveAll observes every listed deployment on the target with one list
// of its app containers (observeStacks).
func (i Inventory) ObserveAll(ctx context.Context, ps []deploy.Placement) ([]deploy.Observation, error) {
	cfg, err := i.Config()
	if err != nil {
		return nil, err
	}
	return observeStacks(ctx, cfg, ps)
}

// The driver implements the contract it is registered under.
var (
	_ deploy.Driver        = (*Driver)(nil)
	_ deploy.FleetObserver = Inventory{}
)

// EndpointCapacity is what the endpoint's Docker says it can give
// containers (its docker info, through Portainer's proxy): memory in bytes
// and CPUs. An endpoint that does not answer is an error, never zero.
func EndpointCapacity(ctx context.Context, cfg *Target, endpointID int64) (memoryBytes int64, cpus int, err error) {
	code, raw, err := JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/info", endpointID), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("asking Portainer's endpoint %d what it has: %w", endpointID, err)
	}
	if code != 200 {
		return 0, 0, fmt.Errorf("asking Portainer's endpoint %d what it has: HTTP %d", endpointID, code)
	}
	var v struct {
		MemTotal int64
		NCPU     int
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.MemTotal <= 0 || v.NCPU <= 0 {
		return 0, 0, fmt.Errorf("Portainer's endpoint %d did not say what it has", endpointID)
	}
	return v.MemTotal, v.NCPU, nil
}

// RecordedRunning is the image the newest released release ran
// (deploy.RunningRecord): a stack carries no AppTown identity, so the
// release's record is the one to hold its app container's image to.
func (d *Driver) RecordedRunning(_ deploy.Placement, released string) string { return released }

// AdoptRunning changes nothing of the handle: the adopted release records
// the image.
func (d *Driver) AdoptRunning(string) map[string]any { return nil }

var _ deploy.RunningRecord = (*Driver)(nil)
