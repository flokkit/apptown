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

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/strs"
)

// Target is a Portainer environment as AppTown reaches it: the API and
// its key resolved at use time, the endpoint's name, the apps domain, and
// the tier name release rows carry.
type Target struct {
	Scope             *Scope // verified by the resolving owner; endpoint name is presentation only
	PortainerURL      string
	PortainerKey      string
	AppsDomain        string
	EnvName           string
	ReleaseDeployment string // the tier name on release rows (default "lan")
	// Installation is this installation's id, carried by every stack it
	// creates or updates as the stack variable InstanceEnv; the inventory
	// counts only stacks that carry it.
	Installation string
}

// InstanceEnv is the stack variable naming the installation that applied a
// stack. Portainer lists a stack's variables with the stack, and they carry
// no meaning for the compose file, which does not use it.
const InstanceEnv = "APPTOWN_INSTANCE"

// stackEnv is the variables a stack is applied with: the installation's mark.
func (cfg *Target) stackEnv() []map[string]string {
	if cfg.Installation == "" {
		return []map[string]string{}
	}
	return []map[string]string{{"name": InstanceEnv, "value": cfg.Installation}}
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
	resp, err := (&http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Portainer API redirect refused: configure the final API URL")
	}}).Do(req)

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
	if cfg.Scope != nil {
		if cfg.Scope.Version != 1 || cfg.Scope.EndpointID <= 0 {
			return 0, errors.New("Portainer endpoint scope is invalid")
		}
		return cfg.Scope.EndpointID, nil
	}
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
	var selected int64
	for _, e := range eps {
		if e.Name == cfg.EnvName {
			if selected != 0 {
				return 0, errors.New("Portainer environment name is ambiguous; select its endpoint ID")
			}
			selected = e.ID
		}
	}
	if selected > 0 {
		return selected, nil
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
	if stacks == nil {
		return nil, errors.New("Portainer stack list is null")
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
func DeployStack(ctx context.Context, cfg *Target, endpointID int64, name, compose string, deploymentID ids.ID, associations []StackAssociation) (StackApplied, error) {
	var applied StackApplied
	err := deployStack(ctx, cfg, endpointID, name, compose, deploymentID, associations, &applied)
	return applied, err
}

func deployStack(ctx context.Context, cfg *Target, endpointID int64, name, compose string, deploymentID ids.ID, associations []StackAssociation, applied *StackApplied) error {
	if cfg.Scope == nil || cfg.Scope.EndpointID != endpointID || deploymentID.IsZero() {
		return errors.New("Portainer mutation requires scoped deployment identity")
	}
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
			var legacy *StackAssociation
			for i := range associations {
				if associations[i].StackID == st.ID {
					legacy = &associations[i]
					break
				}
			}
			if err := VerifyStackAssociation(ctx, *cfg, Stack{ID: st.ID, Name: st.Name, EndpointID: st.EndpointID}, deploymentID, legacy); err != nil {
				return err
			}
			code, raw, err = JSON(ctx, cfg, "PUT",
				fmt.Sprintf("/api/stacks/%d?endpointId=%d", st.ID, endpointID),
				map[string]any{"stackFileContent": compose, "env": cfg.stackEnv(), "pullImage": true, "prune": true})
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
		map[string]any{"name": name, "stackFileContent": compose, "env": cfg.stackEnv()})
	if err == nil && (code == 404 || code == 405) {
		// Older Portainer route shape.
		code, raw, err = JSON(ctx, cfg, "POST",
			fmt.Sprintf("/api/stacks?type=2&method=string&endpointId=%d", endpointID),
			map[string]any{"name": name, "stackFileContent": compose, "env": cfg.stackEnv()})
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

// Driver reconciles an admitted deployment through its owned stack.
// Portainer owns runtime liveness; AppTown records release outcomes.
type Driver struct {
	TargetID ids.ID // AppTown's target record
	Config   func(context.Context, ids.ID) (*Target, error)
	// Owner records the stack when AppTown creates it; nil records
	// nothing, and then take-down deletes no stack.
	Owner deploy.Owner
	// Diagnostics supplies transport to the already verified configuration only.
	Diagnostics      func(context.Context, Target) (Diagnostic, func() error, error)
	Allowed          func(context.Context) error
	Now              func() time.Time
	ReadinessTimeout time.Duration
	ExpectedArtifact string
}

func (d *Driver) Resume(ctx context.Context, p deploy.Placement, generation int64, noted map[string]any, logf func(string, ...any)) (out deploy.Observation, err error) {
	expected, err := savedRelease(noted)
	if err != nil {
		return out, err
	}
	if expected == nil {
		return out, errors.New("AppTown restarted while Portainer was applying the stack; its generation has no retained readiness expectation, so inspect the stack before resolving the attempt")
	}
	if d.ExpectedArtifact == "" || expected.ArtifactHash != d.ExpectedArtifact {
		return out, errors.New("Portainer recovery artifact differs from admitted inputs")
	}
	if expected.Generation != generation {
		return out, errors.New("Portainer recovery checkpoint names another generation")
	}
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return out, err
	}
	diagnostic, close, err := d.diagnostic(ctx, cfg)
	if err != nil {
		return out, err
	}
	defer func() {
		if cleanupErr := close(); cleanupErr != nil {
			invalidateHealth(&out)
			err = errors.Join(err, cleanupErr)
		}
	}()
	p.Handle = noted
	p.Generation = generation
	p.URL = expected.Readiness.URL
	return d.awaitReadiness(ctx, cfg, p, expected, diagnostic)
}

func (d *Driver) Reconcile(ctx context.Context, p deploy.Placement, generation int64, art deploy.Artifact, key string, logf func(string, ...any), checkpoint deploy.Checkpoint) (out deploy.Observation, err error) {
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	cfg, err := d.Config(ctx, d.TargetID)
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
	ready, err := artifactReadiness(string(art.Body))
	if err != nil {
		return out, deploy.Terminal(err)
	}
	var diagnostic *Diagnostic
	if ready != nil {
		if art.Hash == "" || art.Hash != deploy.ConfigHash(strings.TrimSpace(string(art.Body))) {
			return out, deploy.Terminal(errors.New("Portainer artifact differs from its admitted identity"))
		}
		d.ExpectedArtifact = art.Hash
		opened, closeDiagnostic, openErr := d.diagnostic(ctx, cfg)
		if openErr != nil {
			return out, deploy.Terminal(openErr)
		}
		diagnostic = &opened
		defer func() {
			if cleanupErr := closeDiagnostic(); cleanupErr != nil {
				invalidateHealth(&out)
				err = errors.Join(err, cleanupErr)
			}
		}()
	}
	out, err = (Execution{Config: *cfg, TargetID: d.TargetID, DeploymentID: p.ID, EndpointID: endpointID, Stack: stack, Prefix: prefix, Compose: string(art.Body), URL: p.URL, Handle: p.Handle, Log: logf, Owner: d.Owner, Diagnostic: diagnostic, Allowed: d.Allowed}).Execute(ctx, generation, checkpoint)
	if err != nil || ready == nil {
		return out, err
	}
	handle, ok := out.Handle.(map[string]any)
	if !ok {
		return out, errors.New("Portainer apply lost its readiness handle")
	}
	expected, err := savedRelease(handle)
	if err != nil || expected == nil {
		return out, errors.Join(err, errors.New("Portainer apply lost its readiness expectation"))
	}
	p.Handle = handle
	p.Generation = generation
	p.URL = ready.URL
	return d.awaitReadiness(ctx, cfg, p, expected, *diagnostic)
}

// Observe asks Portainer whether the deployment's stack's app container
// runs (observeStacks).
func (d *Driver) Observe(ctx context.Context, p deploy.Placement) (deploy.Observation, error) {
	cfg, err := d.Config(ctx, d.TargetID)
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

// observeStacks reads stack/component state without diagnostic effects. Typed
// expectations verify exact generation, artifact and image identity; historical
// placements can report running state but not release health. A failed look is
// unknown, never stopped. Current readiness comes from the separate owned sample.
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
	stacks, err := ListStacks(ctx, cfg)
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
		found, err := associatedStack(ctx, cfg, p, stacks)
		if err != nil {
			return nil, err
		}
		expected, err := savedRelease(p.Handle)
		if err != nil {
			return nil, err
		}
		if expected != nil {
			if !found {
				return nil, errors.New("Portainer release has no associated stack record")
			}
			out[i], err = observeRelease(ctx, cfg, p, stack, expected)
			if err != nil {
				return nil, err
			}
			continue
		}
		app := apps[strings.ToLower(stack)]
		if !found && app.running {
			return nil, errors.New("Portainer app runs without an associated stack record")
		}
		if app.running {
			out[i].Rollout, out[i].Health = "active", "unknown"
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
	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return out, err
	}
	if cfg.Scope == nil || cfg.Scope.Version != 1 || p.ID.IsZero() {
		return out, errors.New("Portainer teardown requires scoped deployment identity")
	}
	endpointID, err := EndpointID(ctx, cfg)
	if err != nil {
		return out, err
	}
	actual, err := ListStacks(ctx, cfg)
	if err != nil {
		return out, err
	}
	associations, err := SavedAssociations(p.Handle)
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
		present := false
		for _, remote := range actual {
			if fmt.Sprint(remote.ID) != st.ID {
				continue
			}
			present = true
			if remote.Name != st.Name || remote.EndpointID != endpointID {
				return out, errors.New("owned Portainer stack now identifies a different resource")
			}
			var legacy *StackAssociation
			for i := range associations {
				if associations[i].StackID == remote.ID {
					legacy = &associations[i]
					break
				}
			}
			if err := VerifyStackAssociation(ctx, *cfg, remote, p.ID, legacy); err != nil {
				return out, err
			}
		}
		vols, verr := stackVolumes(ctx, cfg, st)
		for name := range listed {
			if !slices.Contains(vols, name) {
				vols = append(vols, name) // a volume an earlier release made
			}
		}
		code := http.StatusNotFound
		var raw []byte
		if present {
			code, raw, err = JSON(ctx, cfg, "DELETE", fmt.Sprintf("/api/stacks/%s?endpointId=%d", url.PathEscape(st.ID), endpointID), nil)
		}
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
// endpoint holds them (composeVolumeNames).
func stackVolumes(ctx context.Context, cfg *Target, st deploy.Owned) ([]string, error) {
	content, err := StackFile(ctx, cfg, st.ID)
	if err != nil {
		return nil, err
	}
	vols, err := composeVolumeNames(st.Name, content)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(vols))
	for i, v := range vols {
		out[i] = v.Name
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
	Diagnostic          *Diagnostic
	Allowed             func(context.Context) error
	TargetID            ids.ID
	DeploymentID        ids.ID
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
	if p.DeploymentID.IsZero() || p.Config.Scope == nil || p.Config.Scope.EndpointID != p.EndpointID {
		return deploy.Observation{}, deploy.Terminal(errors.New("Portainer execution requires verified deployment and endpoint identity"))
	}
	associations, err := SavedAssociations(p.Handle)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	marker, err := composeDeployment(p.Compose)
	if err != nil || marker != p.DeploymentID.String() {
		return deploy.Observation{}, deploy.Terminal(errors.New("Portainer artifact has no matching deployment marker"))
	}
	if p.Stack == "" || p.EndpointID <= 0 {
		return deploy.Observation{}, deploy.Terminal(errors.New("Portainer execution requires a stack and endpoint"))
	}
	stamped, expected, err := stampRelease(p.Compose, p.TargetID, generation, *p.Config.Scope)
	if err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	p.Compose = stamped
	identity := map[string]any{"stack": p.Stack, "endpoint": p.Config.EnvName, "endpointId": p.EndpointID, "providerScope": p.Config.Scope, "deploymentId": p.DeploymentID}
	if expected != nil {
		identity["releaseReadiness"] = expected
	}
	if err := checkpoint(identity); err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("recording Portainer identity: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	if expected != nil {
		if p.Diagnostic == nil || p.Allowed == nil {
			return deploy.Observation{}, deploy.Terminal(errors.New("Portainer release has no admitted diagnostic authority"))
		}
		if err := p.Diagnostic.preflight(ctx, expected.Readiness.Public.ToolImage); err != nil {
			return deploy.Observation{}, deploy.Terminal(err)
		}
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
	allowed := p.Allowed
	if expected != nil {
		allowed = p.Diagnostic.Allowed
	}
	if allowed != nil {
		if err := allowed(ctx); err != nil {
			return deploy.Observation{}, deploy.Terminal(err)
		}
	}
	p.Log("asking Portainer to run generation %d (stack %s on %s)", generation, p.Stack, p.Config.EnvName)
	applied, err := DeployStack(ctx, &p.Config, p.EndpointID, p.Stack, p.Compose, p.DeploymentID, associations)
	if err != nil {
		return deploy.Observation{}, err
	}
	if expected != nil && applied.ID > 0 {
		if err := checkpoint(map[string]any{"stackId": applied.ID, "stackApplied": true}); err != nil {
			return deploy.Observation{}, fmt.Errorf("recording applied Portainer stack: %w", err)
		}
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
	handle["providerScope"] = *p.Config.Scope
	handle["stackId"] = applied.ID
	if expected != nil {
		handle["releaseReadiness"] = expected
	}
	return deploy.Observation{Generation: generation, Rollout: "active", Health: "unknown", URL: p.URL, Handle: handle}, nil
}

// Inventory answers what the Portainer endpoint holds: the stacks it runs,
// as Portainer reports them for the target's endpoint. Built for a target,
// it can look, never deploy. A stack without this installation's variable
// (InstanceEnv) confirms a record only: someone else's stack, or one applied
// before the variable, is never offered as AppTown's leftover.
type Inventory struct {
	Config func(context.Context) (*Target, error)
}

var _ deploy.Inventorier = Inventory{}

func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	cfg, err := i.Config(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.Scope == nil || cfg.Scope.DaemonID == "" {
		return nil, errors.New("Portainer inventory has no verified daemon identity")
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
		Env        []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"Env"`
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
		r := deploy.Resource{Kind: "stack", Name: st.Name, ID: fmt.Sprint(st.ID), Status: status, Stack: st.Name, ObservedDaemonID: cfg.Scope.DaemonID,
			Tags: []string{deploy.TagConfirmsOnly}}
		for _, v := range st.Env {
			if v.Name == InstanceEnv && cfg.Installation != "" && v.Value == cfg.Installation {
				r.Tags = nil
			}
		}
		res = append(res, r)
	}
	return res, nil
}

// ObserveAll observes every listed deployment on the target with one list
// of its app containers (observeStacks).
func (i Inventory) ObserveAll(ctx context.Context, ps []deploy.Placement) ([]deploy.Observation, error) {
	cfg, err := i.Config(ctx)
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

func associatedStack(ctx context.Context, cfg *Target, p deploy.Placement, stacks []Stack) (bool, error) {
	stack, _ := p.Handle["stack"].(string)
	if cfg.Scope == nil || stack == "" {
		return false, errors.New("Portainer observation has no scoped stack")
	}
	associations, err := SavedAssociations(p.Handle)
	if err != nil {
		return false, err
	}
	found := false
	for _, remote := range stacks {
		if remote.Name != stack || remote.EndpointID != cfg.Scope.EndpointID {
			continue
		}
		if found {
			return false, errors.New("Portainer stack identity is ambiguous")
		}
		found = true
		var legacy *StackAssociation
		for j := range associations {
			if associations[j].StackID == remote.ID {
				legacy = &associations[j]
				break
			}
		}
		if err := VerifyStackAssociation(ctx, *cfg, remote, p.ID, legacy); err != nil {
			return false, err
		}
	}
	return found, nil
}
