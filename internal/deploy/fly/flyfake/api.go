// Package flyfake is an in-memory Fly.io Machines API for the Fly driver's
// tests and AppTown's assembled tests. It serves the calls the driver makes
// (apps, Machines, events, secrets, IP assignments, tokens, regions) in
// Fly's shapes (https://docs.fly.io/api/machines/openapi.json), and it can
// hold apps that are not the driver's to touch: another installation's and
// one a person made. A request that reaches one of those is recorded as a
// trespass, so a test can show the driver never touched them.
package flyfake

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Machine is one Machine as the fake keeps it.
type Machine struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	State      string           `json:"state"`
	Region     string           `json:"region"`
	HostStatus string           `json:"host_status"`
	Config     map[string]any   `json:"config"`
	Checks     []map[string]any `json:"checks"`
	Events     []map[string]any `json:"-"`
	Secrets    int64            `json:"-"` // the secrets version it was made with
	Restarts   int              `json:"-"`
	// withheld: Fly cannot reach its host, so it answers config null and
	// the last known config as incomplete_config, or neither when
	// forgotten.
	withheld, forgotten bool
}

// Metadata is the Machine's config metadata.
func (m *Machine) Metadata() map[string]string {
	out := map[string]string{}
	md, _ := m.Config["metadata"].(map[string]any)
	for k, v := range md {
		out[k], _ = v.(string)
	}
	return out
}

// App is one app in the fake's world.
type Volume struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Attached string `json:"attached_machine_id"`
	// Root is what a new volume holds at its root: Fly formats it ext4,
	// with lost+found. Not part of Fly's answer.
	Root []string `json:"-"`
}

type App struct {
	Volumes map[string]*Volume
	// destroyed volumes stay in Fly's volume list, in a destroy state, for
	// their snapshot retention; flyctl hides them unless asked (--all).
	destroyed []*Volume
	ID        string
	Name      string
	Org       string
	Machines  map[string]*Machine
	Secrets   map[string]string
	Version   int64
	IPs       []string
	// Untouchable: any request naming this app is a trespass.
	Untouchable bool
}

// Request names one call.
type Request struct{ Method, Path string }

// API is an isolated Fly world. New makes one.
type API struct {
	mu      sync.Mutex
	apps    map[string]*App
	idem    map[string]string
	next    int
	failing map[Request]int
	// Orgs and AppsOnly are what the token reaches (GET /v1/tokens/current).
	Orgs     []string
	AppsOnly []string
	// Regions are Fly's regions, code to whether a paid plan is needed.
	Regions map[string]bool
	// OnCreate adjusts each Machine as it is made, before it is answered:
	// a test sets its state, checks or exit. The default starts a part
	// Machine with its checks passing, and stops a one-off (restart policy
	// "no") Machine with exit 0.
	OnCreate func(app string, m *Machine)
	// OnExec returns the exec response, including an explicit exit_code;
	// nil represents a missing answer, not a successful completion.
	OnExec func(app string, m *Machine, command []string) (map[string]any, int)
	// Trespasses are the requests that named an untouchable app.
	trespasses []Request
	requests   []Request
	// Taken are app names someone else holds across Fly.
	Taken []string
	// pulls are the image references Machines were made with.
	images []string
	// HoldDeletes answers every Machine deletion 503, as a Fly that does
	// not answer: a test's stand-in for AppTown stopping mid-release.
	HoldDeletes bool
	// cpu and memory are each Machine's metrics, by Machine id.
	cpu     map[string]float64
	memory  map[string][2]int64
	queries []string
}

// SetCPU makes the metrics report the Machine's CPU use: the rate of
// fly_instance_cpu over its guest's own modes, summed over its CPUs, in
// centiseconds per second, as Fly counts it.
func (f *API) SetCPU(id string, centisecondsPerSecond float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cpu[id] = centisecondsPerSecond
}

// SetMemory makes the metrics report the Machine's guest memory, in bytes.
func (f *API) SetMemory(id string, total, available int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memory[id] = [2]int64{total, available}
}

// Queries are the metrics queries asked, in order.
func (f *API) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// Unreachable makes Fly lose the Machine's host: it answers host_status
// unreachable and config null, with its last known config as
// incomplete_config unless forgotten.
func (f *API) Unreachable(app, id string, forgotten bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.apps[app].Machines[id]
	m.HostStatus, m.withheld, m.forgotten = "unreachable", true, forgotten
}

// AddEvent records an event of a Machine, as Fly keeps them; a Machine
// answer carries the newest five.
func (f *API) AddEvent(app, id string, e map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.apps[app].Machines[id]
	m.Events = append(m.Events, e)
}

// New is a world with one org the token reaches.
func New(org string) *API {
	return &API{apps: map[string]*App{}, idem: map[string]string{}, failing: map[Request]int{}, cpu: map[string]float64{}, memory: map[string][2]int64{}, Orgs: []string{org},
		Regions: map[string]bool{"syd": true, "iad": true, "jnb": false}}
}

// SeedApp puts an app the driver did not make in the world, with Machines
// carrying metadata (another installation's marks, or none). Untouchable
// apps record every request that names them.
func (f *API) SeedApp(org, name string, untouchable bool, metadata ...map[string]string) *App {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.newApp(org, name)
	a.Untouchable = untouchable
	for _, md := range metadata {
		m := &Machine{ID: f.id("m"), Name: name + "-machine", State: "started", HostStatus: "ok", Config: map[string]any{"metadata": toAny(md), "image": "example/app:1"}}
		a.Machines[m.ID] = m
	}
	return a
}

// App is the app of that name, a copy; nil when there is none.
func (f *API) App(name string) *App {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.apps[name]
	if a == nil {
		return nil
	}
	cp := *a
	cp.Volumes = map[string]*Volume{}
	for id, v := range a.Volumes {
		vc := *v
		vc.Root = append([]string(nil), v.Root...)
		cp.Volumes[id] = &vc
	}
	cp.Machines = map[string]*Machine{}
	for id, m := range a.Machines {
		mc := *m
		cp.Machines[id] = &mc
	}
	return &cp
}

// Apps are the names of every app.
func (f *API) Apps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.apps {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Set changes a Machine's state, host status and check status.
func (f *API) Set(app, id, state, hostStatus, check string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.apps[app].Machines[id]
	if state != "" {
		m.State = state
	}
	if hostStatus != "" {
		m.HostStatus = hostStatus
	}
	if check != "" {
		for _, c := range m.Checks {
			c["status"] = check
		}
	}
}

// Fail answers the next n requests of method and path with status.
func (f *API) Fail(method, path string, status, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[Request{method, path}] = status*1000 + n
}

// Trespasses are the requests that named an app the driver must leave alone.
func (f *API) Trespasses() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.trespasses)
}

// Requests are every request, in order.
func (f *API) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Images are the image references Machines were made with.
func (f *API) Images() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.images)
}

func (f *API) id(prefix string) string {
	f.next++
	return fmt.Sprintf("%s%06x", prefix, f.next)
}

func (f *API) newApp(org, name string) *App {
	a := &App{ID: f.id("app"), Name: name, Org: org, Machines: map[string]*Machine{}, Volumes: map[string]*Volume{}, Secrets: map[string]string{}}
	f.apps[name] = a
	return a
}

func toAny(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Handler serves the Machines API.
func (f *API) Handler() http.Handler {
	return http.HandlerFunc(f.serve)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func refuse(w http.ResponseWriter, status int, code, msg string) {
	write(w, status, map[string]any{"error": msg, "status": code})
}

func (f *API) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/prometheus/") {
		f.metrics(w, r)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		refuse(w, 401, "", "unauthorized")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	req := Request{r.Method, r.URL.Path}
	f.requests = append(f.requests, req)
	if v, ok := f.failing[req]; ok {
		status, n := v/1000, v%1000
		if n <= 1 {
			delete(f.failing, req)
		} else {
			f.failing[req] = status*1000 + n - 1
		}
		refuse(w, status, "", "failing as the test asked")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "apps" {
		if a := f.apps[parts[2]]; a != nil && a.Untouchable {
			f.trespasses = append(f.trespasses, req)
		}
	}
	var body map[string]any
	if raw, err := io.ReadAll(r.Body); err != nil {
		refuse(w, 400, "", "reading the body: "+err.Error())
		return
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			refuse(w, 400, "", "the body is not JSON: "+err.Error())
			return
		}
	}
	switch {
	case r.URL.Path == "/v1/tokens/current":
		var tokens []map[string]any
		for _, o := range f.Orgs {
			tokens = append(tokens, map[string]any{"org_slug": o})
		}
		if len(f.AppsOnly) > 0 {
			tokens = append(tokens, map[string]any{"org_slug": "", "apps": f.AppsOnly})
		}
		write(w, 200, map[string]any{"tokens": tokens})
	case r.URL.Path == "/v1/platform/regions":
		var rows []map[string]any
		for code, paid := range f.Regions {
			rows = append(rows, map[string]any{"code": code, "name": code, "requires_paid_plan": paid})
		}
		write(w, 200, map[string]any{"regions": rows})
	case len(parts) == 4 && parts[1] == "orgs" && parts[3] == "machines":
		var out []map[string]any
		for _, a := range f.apps {
			if a.Org != parts[2] {
				continue
			}
			for _, m := range a.Machines {
				out = append(out, map[string]any{"id": m.ID, "name": m.Name, "app_name": a.Name, "state": m.State, "config": map[string]any{"metadata": m.Config["metadata"]}})
			}
		}
		write(w, 200, map[string]any{"machines": out})
	case r.URL.Path == "/v1/apps" && r.Method == "GET":
		org := r.URL.Query().Get("org_slug")
		var out []map[string]any
		for _, a := range f.apps {
			if a.Org == org {
				out = append(out, map[string]any{"id": a.ID, "name": a.Name, "status": "deployed", "machine_count": len(a.Machines), "organization": map[string]any{"slug": a.Org}})
			}
		}
		write(w, 200, map[string]any{"apps": out})
	case r.URL.Path == "/v1/apps" && r.Method == "POST":
		f.createApp(w, body)
	case len(parts) == 3 && parts[1] == "apps":
		f.app(w, r, parts[2])
	case len(parts) >= 4 && parts[1] == "apps":
		a := f.apps[parts[2]]
		if a == nil {
			refuse(w, 404, "", "app not found")
			return
		}
		f.appResource(w, r, a, parts[3:], body)
	default:
		refuse(w, 404, "", "no such endpoint in the fake: "+r.Method+" "+r.URL.Path)
	}
}

func (f *API) createApp(w http.ResponseWriter, body map[string]any) {
	name, _ := body["name"].(string)
	org, _ := body["org_slug"].(string)
	key, _ := body["idempotency_key"].(string)
	if key != "" {
		if prev, ok := f.idem[key]; ok {
			if prev != name {
				refuse(w, 422, "", "idempotency key used with another name")
				return
			}
			if a := f.apps[prev]; a != nil {
				write(w, 201, map[string]any{"id": a.ID, "created_at": 0})
				return
			}
		}
	}
	if f.apps[name] != nil || slices.Contains(f.Taken, name) {
		refuse(w, 422, "name_taken", "app name "+name+" is already taken")
		return
	}
	if !slices.Contains(f.Orgs, org) {
		refuse(w, 404, "", "organization not found")
		return
	}
	a := f.newApp(org, name)
	if key != "" {
		f.idem[key] = name
	}
	write(w, 201, map[string]any{"id": a.ID, "created_at": 0})
}

func (f *API) app(w http.ResponseWriter, r *http.Request, name string) {
	a := f.apps[name]
	if a == nil {
		refuse(w, 404, "", "app not found")
		return
	}
	switch r.Method {
	case "GET":
		write(w, 200, map[string]any{"id": a.ID, "name": a.Name, "status": "deployed", "machine_count": len(a.Machines), "organization": map[string]any{"slug": a.Org}})
	case "DELETE":
		delete(f.apps, name)
		write(w, 202, nil)
	default:
		refuse(w, 405, "", "method")
	}
}

func (f *API) appResource(w http.ResponseWriter, r *http.Request, a *App, rest []string, body map[string]any) {
	switch rest[0] {
	case "secrets":
		f.secrets(w, r, a, body)
	case "ip_assignments":
		if r.Method == "POST" {
			kind, _ := body["type"].(string)
			ip := "137.66.0." + strconv.Itoa(len(a.IPs)+1)
			if kind == "v6" {
				ip = "2a09:8280:1::" + strconv.Itoa(len(a.IPs)+1)
			}
			a.IPs = append(a.IPs, ip)
			write(w, 200, map[string]any{"ip": ip, "shared": kind == "shared_v4"})
			return
		}
		var ips []map[string]any
		for _, ip := range a.IPs {
			ips = append(ips, map[string]any{"ip": ip})
		}
		write(w, 200, map[string]any{"ips": ips})
	case "volumes":
		if len(rest) == 1 {
			if r.Method == "POST" {
				name, _ := body["name"].(string)
				v := &Volume{ID: f.id("vol"), Name: name, State: "created", Root: []string{"lost+found"}}
				a.Volumes[v.ID] = v
				write(w, 200, v)
				return
			}
			vs := []*Volume{}
			for _, v := range a.Volumes {
				vs = append(vs, v)
			}
			vs = append(vs, a.destroyed...)
			write(w, 200, vs)
			return
		}
		v := a.Volumes[rest[1]]
		if v == nil {
			refuse(w, 404, "", "volume not found")
			return
		}
		if r.Method == "DELETE" {
			if v.Attached != "" {
				refuse(w, 409, "", "volume attached")
				return
			}
			delete(a.Volumes, v.ID)
			a.destroyed = append(a.destroyed, &Volume{ID: v.ID, Name: v.Name, State: "pending_destroy"})
			write(w, 200, nil)
			return
		}
		write(w, 200, v)
	case "machines":
		f.machines(w, r, a, rest[1:], body)
	default:
		refuse(w, 404, "", "no such endpoint in the fake")
	}
}

func (f *API) secrets(w http.ResponseWriter, r *http.Request, a *App, body map[string]any) {
	if r.Method == "GET" {
		var out []map[string]any
		for k := range a.Secrets {
			out = append(out, map[string]any{"name": k})
		}
		write(w, 200, map[string]any{"secrets": out})
		return
	}
	values, _ := body["values"].(map[string]any)
	for k, v := range values {
		if v == nil {
			delete(a.Secrets, k)
			continue
		}
		a.Secrets[k], _ = v.(string)
	}
	a.Version++
	write(w, 200, map[string]any{"version": a.Version})
}

func (f *API) machines(w http.ResponseWriter, r *http.Request, a *App, rest []string, body map[string]any) {
	if len(rest) == 0 {
		if r.Method == "POST" {
			f.createMachine(w, a, body)
			return
		}
		var ms []*Machine
		for _, m := range a.Machines {
			ms = append(ms, m)
		}
		slices.SortFunc(ms, func(x, y *Machine) int { return strings.Compare(x.ID, y.ID) })
		out := []map[string]any{}
		for _, m := range ms {
			out = append(out, answered(m))
		}
		write(w, 200, out)
		return
	}
	m := a.Machines[rest[0]]
	if m == nil {
		refuse(w, 404, "", "machine not found")
		return
	}
	switch {
	case len(rest) == 1 && r.Method == "POST":
		cfg, _ := body["config"].(map[string]any)
		mounts, _ := cfg["mounts"].([]any)
		for _, v := range a.Volumes {
			if v.Attached == m.ID {
				found := false
				for _, mt := range mounts {
					if mt.(map[string]any)["volume"] == v.ID {
						found = true
					}
				}
				if !found {
					refuse(w, 422, "", "update must retain its mount")
					return
				}
			}
		}
		m.Config = cfg
		m.State = "started"
		write(w, 200, m)
	case len(rest) == 2 && rest[1] == "exec":
		var command []string
		for _, v := range body["command"].([]any) {
			command = append(command, v.(string))
		}
		answer, status := map[string]any{"exit_code": 0}, 200
		if f.OnExec != nil {
			answer, status = f.OnExec(a.Name, m, command)
		}
		write(w, status, answer)
	case len(rest) == 1 && r.Method == "GET":
		write(w, 200, answered(m))
	case len(rest) == 1 && r.Method == "DELETE" && f.HoldDeletes:
		refuse(w, 503, "", "held")
	case len(rest) == 1 && r.Method == "DELETE":
		for _, v := range a.Volumes {
			if v.Attached == m.ID {
				v.Attached = ""
			}
		}
		delete(a.Machines, m.ID)
		write(w, 200, map[string]any{"ok": true})
	case len(rest) == 2 && rest[1] == "stop":
		m.State = "stopped"
		write(w, 200, nil)
	case len(rest) == 2 && rest[1] == "start":
		m.State = "started"
		write(w, 200, nil)
	case len(rest) == 2 && rest[1] == "events":
		write(w, 200, m.Events)
	case len(rest) == 2 && rest[1] == "restart":
		m.Restarts++
		m.State = "started"
		write(w, 200, map[string]any{"ok": true})
	default:
		refuse(w, 404, "", "no such machine endpoint in the fake")
	}
}

func (f *API) createMachine(w http.ResponseWriter, a *App, body map[string]any) {
	cfg, _ := body["config"].(map[string]any)
	img, _ := cfg["image"].(string)
	if !strings.HasPrefix(img, "registry.fly.io/"+a.Name+"@sha256:") {
		refuse(w, 422, "", "image "+img+" is not in this app's repository in Fly's registry")
		return
	}
	if mounts, _ := cfg["mounts"].([]any); len(mounts) > 0 {
		for _, mt := range mounts {
			id, _ := mt.(map[string]any)["volume"].(string)
			v := a.Volumes[id]
			if v == nil || v.Attached != "" {
				refuse(w, 409, "", "volume absent or already attached")
				return
			}
		}
	}
	f.images = append(f.images, img)
	name, _ := body["name"].(string)
	region, _ := body["region"].(string)
	m := &Machine{ID: f.id("m"), Name: name, Region: region, State: "started", HostStatus: "ok", Config: cfg}
	if v, ok := body["min_secrets_version"].(float64); ok {
		m.Secrets = int64(v)
	}
	if rp, _ := cfg["restart"].(map[string]any); rp["policy"] == "no" {
		m.State = "stopped"
		m.Events = []map[string]any{{"type": "exit", "status": "stopped", "timestamp": 1, "request": map[string]any{"exit_event": map[string]any{"exit_code": 0, "oom_killed": false, "signal": -1}}}}
	}
	if svcs, ok := cfg["services"].([]any); ok {
		for _, s := range svcs {
			sm, _ := s.(map[string]any)
			if checks, ok := sm["checks"].([]any); ok {
				for i := range checks {
					m.Checks = append(m.Checks, map[string]any{"name": "servicecheck-" + strconv.Itoa(i), "status": "passing"})
				}
			}
		}
	}
	if checks, ok := cfg["checks"].(map[string]any); ok {
		for name := range checks {
			m.Checks = append(m.Checks, map[string]any{"name": name, "status": "passing"})
		}
	}
	// Fly answers a service's autostop as a boolean (false for "off",
	// true for "stop"), whatever form the request gave it.
	if svcs, ok := cfg["services"].([]any); ok {
		for _, s := range svcs {
			if sm, ok := s.(map[string]any); ok {
				switch sm["autostop"] {
				case "off":
					sm["autostop"] = false
				case "stop":
					sm["autostop"] = true
				}
			}
		}
	}
	if body["skip_launch"] == true {
		m.State = "stopped"
	}
	if f.OnCreate != nil {
		f.OnCreate(a.Name, m)
	}
	a.Machines[m.ID] = m
	if mounts, _ := cfg["mounts"].([]any); len(mounts) > 0 {
		for _, mt := range mounts {
			id, _ := mt.(map[string]any)["volume"].(string)
			a.Volumes[id].Attached = m.ID
		}
	}
	write(w, 200, m)
}

// Exit makes a one-off Machine's exit code and out-of-memory flag. Like
// Fly, it reports signal -1 when no signal ended the Machine.
func Exit(m *Machine, code int, oom bool) {
	m.State = "stopped"
	m.Events = []map[string]any{{"type": "exit", "status": "stopped", "timestamp": 1, "request": map[string]any{"exit_event": map[string]any{"exit_code": code, "oom_killed": oom, "signal": -1}}}}
}

// PathOf is an API path for an app's resource, for Fail.
func PathOf(app string, rest ...string) string {
	p := "/v1/apps/" + url.PathEscape(app)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// Adopt puts a copy of m into the app named to, as a person might add a
// Machine of their own to an app, and answers the copy's id.
func (f *API) Adopt(to string, m *Machine) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *m
	cp.ID = f.id("m")
	f.apps[to].Machines[cp.ID] = &cp
	return cp.ID
}

// SetMetadata sets one metadata key of a Machine's config.
func (f *API) SetMetadata(app, id, key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.apps[app].Machines[id]
	cfg := map[string]any{}
	for k, v := range m.Config {
		cfg[k] = v
	}
	md := map[string]any{}
	if old, ok := cfg["metadata"].(map[string]any); ok {
		for k, v := range old {
			md[k] = v
		}
	}
	md[key] = value
	cfg["metadata"] = md
	m.Config = cfg
}

// answered is a Machine as Fly answers it: with up to five of its newest
// events, newest first.
func answered(m *Machine) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	events := slices.Clone(m.Events)
	slices.SortStableFunc(events, func(x, y map[string]any) int { return cmpTimestamp(y, x) })
	if len(events) > 5 {
		events = events[:5]
	}
	out["events"] = events
	if m.withheld {
		out["config"] = nil
		if !m.forgotten {
			out["incomplete_config"] = m.Config
		}
	}
	return out
}

func cmpTimestamp(x, y map[string]any) int {
	stamp := func(e map[string]any) int64 {
		switch v := e["timestamp"].(type) {
		case int:
			return int64(v)
		case int64:
			return v
		case float64:
			return int64(v)
		}
		return 0
	}
	return cmp.Compare(stamp(x), stamp(y))
}

// metrics serves GET /prometheus/{org}/api/v1/query for the two series
// the driver asks for, from the metrics a test set (SetCPU, SetMemory), scoped to the app the
// query names. It answers only a token in Fly's forms and an org the token
// reaches. Fail can make it refuse.
func (f *API) metrics(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := Request{r.Method, r.URL.Path}
	f.requests = append(f.requests, req)
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") && !strings.HasPrefix(auth, "FlyV1 ") {
		refuse(w, 401, "", "unauthorized")
		return
	}
	if v, ok := f.failing[req]; ok {
		status, n := v/1000, v%1000
		if n <= 1 {
			delete(f.failing, req)
		} else {
			f.failing[req] = status*1000 + n - 1
		}
		refuse(w, status, "", "failing as the test asked")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[2] != "api" || parts[4] != "query" || !slices.Contains(f.Orgs, parts[1]) {
		refuse(w, 404, "", "no such metrics endpoint in the fake")
		return
	}
	q := r.URL.Query().Get("query")
	f.queries = append(f.queries, q)
	app := ""
	if _, rest, ok := strings.Cut(q, `app="`); ok {
		app, _, _ = strings.Cut(rest, `"`)
	}
	result := []map[string]any{}
	if a := f.apps[app]; a != nil {
		for id := range a.Machines {
			var v float64
			if strings.Contains(q, "fly_instance_memory_mem_total") {
				mem, ok := f.memory[id]
				if !ok {
					continue
				}
				v = float64(mem[0] - mem[1])
			} else {
				rate, ok := f.cpu[id]
				if !ok {
					continue
				}
				// The fake keeps no series: it answers the rate, and
				// divides it as the query does.
				v = rate
				if strings.HasSuffix(strings.TrimSpace(q), "/ 100") {
					v /= 100
				}
			}
			result = append(result, map[string]any{"metric": map[string]string{"app": app, "instance": id}, "value": []any{1, strconv.FormatFloat(v, 'f', -1, 64)}})
		}
	}
	write(w, 200, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
}
