package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

func TestInfrastructureRemovalChecksFreshSharedPlacement(t *testing.T) {
	w := newTargetHTTPWorld(t)
	host := w.seedBuiltin(t)
	projectID := w.createProject(t, "shared-app")
	unavailable := false
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if unavailable {
			out.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(out, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(out, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(out, `{"ID":"engine-a"}`)
		default:
			t.Errorf("unexpected provider request %s", r.URL)
			out.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	writeTargetSecret(t, w, "PT_URL="+server.URL+"\nPT_KEY=scope-secret\n")
	if code, out := targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", `{"kind":"portainer","name":"customer-office","urlFrom":"PT_URL","apiKeyFrom":"PT_KEY","environment":"docker"}`, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("attach: %d %+v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "customer-office")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.deployment.Deployments.EnsureStanding(t.Context(), projectID, "blue", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	stack := portainer.StackName(portainer.StackNames(dep.ID))
	sharedName := "apptown-need-" + stack + "-postgres"
	orphanName := "apptown-data-orphan"
	daemonID := "engine-a"
	removed := 0
	failInventory := false
	w.setDockerClient(targetDockerClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		switch {
		case strings.HasPrefix(line, "info "):
			if failInventory {
				return nil, fmt.Errorf("inventory unavailable")
			}
			return []byte(daemonID), nil
		case strings.HasPrefix(line, "volume ls"):
			return []byte(sharedName + "\t\t" + stack + "\t" + testInstallation + "\n" + orphanName + "\t\t\t" + testInstallation + "\n"), nil
		case strings.HasPrefix(line, "volume rm"):
			removed++
			return nil, nil
		default:
			return nil, nil
		}
	}))
	observed := []deploy.Resource{{Kind: "volume", Name: sharedName, Stack: stack, Record: deploy.RecordUnrecorded, ObservedDaemonID: "engine-a"}}
	out, note, err := checkedLeftovers(t, w.infra, host, observed)
	if err != nil || len(out) != 0 || !strings.Contains(note, target.Name) || !strings.Contains(note, "shared-app/blue") {
		t.Fatalf("customer placement: %+v %q %v", out, note, err)
	}
	encoded, _ := json.Marshal(observed)
	if strings.Contains(string(encoded), "engine-a") {
		t.Fatal("private observation identity leaked into API")
	}
	merged := deployment.MergeInventory(nil, observed)
	if len(merged) != 1 || merged[0].ObservedDaemonID != "engine-a" {
		t.Fatal("merge lost observed identity")
	}
	remove := func(name string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"kind": "volume", "name": name, "confirm": name})
		code, result := targetCall(t, w.infra.RemoveInfrastructure, "POST", "/api/v1/infrastructure/remove", string(body), map[string]string{"target": host.Name})
		if code != want {
			t.Fatalf("remove %s: %d want %d: %+v", name, code, want, result)
		}
	}
	// Direct POST has never fetched GET's hidden resource list.
	remove(sharedName, http.StatusConflict)
	unavailable = true
	remove(sharedName, http.StatusServiceUnavailable)
	unavailable = false
	failInventory = true
	remove(orphanName, http.StatusServiceUnavailable)
	failInventory = false
	remove("apptown-data-not-observed", http.StatusConflict)
	if removed != 0 {
		t.Fatalf("refused removal reached Docker %d times", removed)
	}
	// A verified distinct daemon does not inherit another environment's claim.
	daemonID = "different-daemon"
	remove(sharedName, http.StatusOK)
	remove(orphanName, http.StatusOK)
	if removed != 2 {
		t.Fatalf("ordinary verified orphans not removed: %d", removed)
	}
	if observed[0].ObservedDaemonID != "engine-a" {
		t.Fatal("response shaping mutated cached evidence")
	}
}

// The fixture collects provider evidence before the page reads it.
type siblingInventoryCache map[string]struct {
	items  []deploy.Resource
	shared []deployment.SharedResource
	error  string
}

func (c siblingInventoryCache) Live(_ context.Context, t *deployment.Target) ([]deploy.Resource, []deployment.SharedResource, string, time.Time, bool, bool) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	if c[t.Name].error != "" {
		at = time.Time{}
	}
	return c[t.Name].items, c[t.Name].shared, c[t.Name].error, at, false, at.IsZero()
}
func collectSiblingInventory(t *testing.T, w *targetHTTPWorld) siblingInventoryCache {
	t.Helper()
	targets, err := w.deployment.Targets.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := siblingInventoryCache{}
	for _, target := range targets {
		inv, err := w.providers.TargetInventory(target)
		if err != nil {
			continue
		}
		items, err := inv.Inventory(t.Context())
		var shared []deployment.SharedResource
		if err == nil {
			shared, err = w.deployment.Inventory.Shared(t.Context(), items)
		}
		why := ""
		if err != nil {
			why = err.Error()
			items = nil
		}
		out[target.Name] = struct {
			items  []deploy.Resource
			shared []deployment.SharedResource
			error  string
		}{items, shared, why}
	}
	return out
}

type siblingStack struct {
	ID         int64            `json:"Id"`
	Name       string           `json:"Name"`
	EndpointID int64            `json:"EndpointId"`
	Env        []map[string]any `json:"Env"`
}

// ours is the stack variable this installation applies its stacks with.
var ours = []map[string]any{{"name": portainer.InstanceEnv, "value": testInstallation}}

type siblingInventoryWorld struct {
	w                              *targetHTTPWorld
	targets                        [2]*deployment.Target
	stacks                         []siblingStack
	mu                             sync.Mutex
	failList, failOwner, duplicate bool
	daemon                         string
	deleted                        []string
	requests                       int
}

func newSiblingInventoryWorld(t *testing.T) *siblingInventoryWorld {
	t.Helper()
	f := &siblingInventoryWorld{w: newTargetHTTPWorld(t), daemon: "shared-engine"}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if f.failOwner && r.Header.Get("X-API-Key") == "key-b" {
			out.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch {
		case r.URL.Path == "/api/endpoints":
			fmt.Fprint(out, `[{"Id":3,"Name":"docker","Type":1}]`)
		case r.URL.Path == "/api/endpoints/3":
			fmt.Fprint(out, `{"Id":3,"Name":"docker","Type":1}`)
		case r.URL.Path == "/api/endpoints/3/docker/info":
			fmt.Fprintf(out, `{"ID":%q}`, f.daemon)
		case r.URL.Path == "/api/stacks" && r.Method == http.MethodGet:
			if f.failList {
				out.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			stacks := append([]siblingStack{}, f.stacks...)
			if f.duplicate {
				stacks = append(stacks, f.stacks[1])
			}
			if err := json.NewEncoder(out).Encode(stacks); err != nil {
				t.Error(err)
			}
		case strings.HasPrefix(r.URL.Path, "/api/stacks/") && r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, r.URL.RequestURI())
			fmt.Fprint(out, `{}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
			out.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeTargetSecret(t, f.w, "PT_URL="+server.URL+"\nPT_A=key-a\nPT_B=key-b\n")
	for i, name := range []string{"customer-a", "customer-b"} {
		body := `{"kind":"portainer","name":"` + name + `","urlFrom":"PT_URL","apiKeyFrom":"` + []string{"PT_A", "PT_B"}[i] + `","environment":"docker"}`
		if code, out := targetCall(t, f.w.targets.AttachTarget, "POST", "/api/v1/targets", body, nil); code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("attach %s: %d %+v", name, code, out)
		}
		target, err := f.w.deployment.Targets.ByName(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		f.targets[i] = target
	}
	projectID := f.w.createProject(t, "sibling-app")
	for i, target := range f.targets {
		dep, err := f.w.deployment.Deployments.EnsureStanding(t.Context(), projectID, target.Name, target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
		if err != nil {
			t.Fatal(err)
		}
		stack := portainer.StackName(portainer.StackNames(dep.ID))
		if err := f.w.deployment.Deployments.RecordOwned(t.Context(), dep.ID, "stack", stack); err != nil {
			t.Fatal(err)
		}
		f.stacks = append(f.stacks, siblingStack{ID: int64(i + 7), Name: stack, EndpointID: 3, Env: ours})
	}
	f.stacks = append(f.stacks, siblingStack{ID: 9, Name: "ordinary-orphan", EndpointID: 3, Env: ours})
	f.w.infra.cache = collectSiblingInventory(t, f.w)
	return f
}

func (f *siblingInventoryWorld) remove(t *testing.T, name, id string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"kind": "stack", "name": name, "id": id, "confirm": name})
	if err != nil {
		t.Fatal(err)
	}
	return targetCall(t, f.w.infra.RemoveInfrastructure, "POST", "/api/v1/infrastructure/customer-a/remove", string(body), map[string]string{"target": f.targets[0].Name})
}

func TestPortainerSiblingStackIsNotAnInfrastructureOrphan(t *testing.T) {
	f := newSiblingInventoryWorld(t)
	before := f.requests
	code, answer := targetCall(t, f.w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	if f.requests != before {
		t.Fatal("infrastructure page called provider for placement")
	}
	if code != http.StatusOK {
		t.Fatalf("inventory: %d %+v", code, answer)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var view InfrastructureTargets
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	for _, target := range view.Targets {
		if target.Target != f.targets[0].Name {
			continue
		}
		orphan := false
		for _, resource := range target.Resources {
			if resource.Name == f.stacks[1].Name && resource.Record == deploy.RecordUnrecorded {
				t.Fatal("live sibling stack offered as an infrastructure orphan")
			}
			orphan = orphan || resource.Name == "ordinary-orphan" && resource.Record == deploy.RecordUnrecorded
		}
		if !orphan || !strings.Contains(target.Shared, f.targets[1].Name) {
			t.Fatalf("ordinary orphan or sibling attribution lost: %+v", target)
		}
		if strings.Contains(string(body), "shared-engine") {
			t.Fatal("private daemon observation leaked")
		}
		return
	}
	t.Fatal("first customer target missing")
}

func TestPortainerSiblingStackCannotBeRemovedThroughAnotherTarget(t *testing.T) {
	f := newSiblingInventoryWorld(t)
	// No Infrastructure GET precedes this direct POST.
	code, answer := f.remove(t, f.stacks[1].Name, strconv.FormatInt(f.stacks[1].ID, 10))
	if code != http.StatusConflict {
		t.Fatalf("live sibling removal: %d %+v", code, answer)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 0 {
		t.Fatalf("sibling refusal reached provider DELETE: %v", f.deleted)
	}
}

func TestPortainerOrphanRemovalRequiresFreshExactObservation(t *testing.T) {
	f := newSiblingInventoryWorld(t)
	check := func(name, id string, want int) {
		t.Helper()
		code, answer := f.remove(t, name, id)
		if code != want {
			t.Fatalf("remove %s/%s: %d want %d: %+v", name, id, code, want, answer)
		}
	}
	check("ordinary-orphan", "8", http.StatusConflict)
	check("missing-stack", "90", http.StatusConflict)
	check("ordinary-orphan", "", http.StatusBadRequest)
	f.mu.Lock()
	f.duplicate = true
	f.mu.Unlock()
	check(f.stacks[1].Name, "8", http.StatusConflict)
	f.mu.Lock()
	f.duplicate = false
	f.failList = true
	f.mu.Unlock()
	check("ordinary-orphan", "9", http.StatusServiceUnavailable)
	f.mu.Lock()
	f.failList = false
	f.failOwner = true
	f.mu.Unlock()
	check(f.stacks[1].Name, "8", http.StatusServiceUnavailable)
	f.mu.Lock()
	f.failOwner = false
	f.daemon = ""
	f.mu.Unlock()
	check("ordinary-orphan", "9", http.StatusBadGateway)
	f.mu.Lock()
	f.daemon = "shared-engine"
	n := len(f.deleted)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("unverified removal reached provider DELETE %d times", n)
	}
	check("ordinary-orphan", "9", http.StatusOK)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 1 || f.deleted[0] != "/api/stacks/9?endpointId=3" {
		t.Fatalf("ordinary orphan removal: %v", f.deleted)
	}
}

// A missing provider association makes that daemon's ownership classification
// unknown; it must not hide the rest of the fleet or create orphan authority.
func TestUnverifiedPlacementKeepsOtherInfrastructureVisible(t *testing.T) {
	f := newSiblingInventoryWorld(t)
	builtin := f.w.seedBuiltin(t)
	legacy, err := f.w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: "legacy-unbound", Kind: deployment.KindPortainer, Status: deployment.TargetReady, Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": "PT_B", "environment": "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	pid := f.w.createProject(t, "legacy-app")
	dep, err := f.w.deployment.Deployments.EnsureStanding(t.Context(), pid, "office", legacy.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	stack := portainer.StackName(portainer.StackNames(dep.ID))
	if err := f.w.deployment.Deployments.RecordOwned(t.Context(), dep.ID, "stack", stack); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.stacks = append(f.stacks, siblingStack{ID: 10, Name: stack, EndpointID: 3, Env: ours})
	f.mu.Unlock()
	// No infrastructure GET precedes this direct mutation: read availability
	// cannot supply the verified placement that deletion requires.
	code, answer := f.remove(t, stack, "10")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unverified placement removal: %d %+v", code, answer)
	}
	f.w.infra.cache = collectSiblingInventory(t, f.w)
	code, answer = targetCall(t, f.w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	if code != http.StatusOK {
		t.Fatalf("one unverified placement hid the fleet: %d %+v", code, answer)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var view InfrastructureTargets
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	targets := map[string]InfrastructureView{}
	for _, v := range view.Targets {
		targets[v.Target] = v
	}
	if len(targets) != 4 {
		t.Fatalf("targets disappeared: %+v", targets)
	}
	healthy := targets[builtin.Name]
	if !healthy.Live || healthy.Error != "" {
		t.Fatalf("unrelated built-in inventory lost: %+v", healthy)
	}
	for _, name := range []string{f.targets[0].Name, f.targets[1].Name, legacy.Name} {
		v := targets[name]
		if v.Live || v.Error == "" || !v.AsOf.IsZero() || v.Shared != "" {
			t.Fatalf("unknown inventory claimed current classification: %+v", v)
		}
		if len(v.Resources) != 1 || v.Resources[0].Record != deploy.RecordRecorded {
			t.Fatalf("fallback is not records only: %+v", v)
		}
		if name != legacy.Name && !strings.Contains(v.Error, "verifying legacy-unbound placement") {
			t.Fatalf("placement failure hidden: %+v", v)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 0 {
		t.Fatalf("unverified placement reached provider DELETE: %v", f.deleted)
	}
}

// A kind with no inventory reader is unknown at its target: not an error,
// not live, never an empty success.
func TestKindWithoutInventoryReaderIsUnknownNotAnError(t *testing.T) {
	w := newTargetHTTPWorld(t)
	if _, err := w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: "unsupported-provider", Kind: "operator-provider", Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	code, answer := targetCall(t, w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	if code != http.StatusOK {
		t.Fatalf("adapter failure hid the fleet: %d %+v", code, answer)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var view InfrastructureTargets
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Targets) != 1 || view.Targets[0].Live || !view.Targets[0].Unreported || view.Targets[0].Error != "" || len(view.Targets[0].Resources) != 0 {
		t.Fatalf("a kind without a reader is not shown as unknown: %+v", view)
	}
}

// AppTown owns what carries this installation's mark. Another
// installation's stack and one applied before the mark are neither listed
// as not accounted for nor removable; a recorded stack without the mark
// stays recorded, never missing.
func TestPortainerStackWithoutThisInstallationsMarkIsNotAnOrphan(t *testing.T) {
	f := newSiblingInventoryWorld(t)
	f.mu.Lock()
	f.stacks[0].Env = nil // recorded, applied before the mark
	f.stacks = append(f.stacks,
		siblingStack{ID: 11, Name: "other-installations", EndpointID: 3, Env: []map[string]any{{"name": portainer.InstanceEnv, "value": "inst-other"}}},
		siblingStack{ID: 12, Name: "before-the-mark", EndpointID: 3})
	f.mu.Unlock()
	code, answer := targetCall(t, f.w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	if code != http.StatusOK {
		t.Fatalf("inventory: %d %+v", code, answer)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var view InfrastructureTargets
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range view.Targets {
		if target.Target != f.targets[0].Name {
			continue
		}
		found = true
		records := map[string]string{}
		for _, r := range target.Resources {
			records[r.Name] = r.Record
		}
		if records[f.stacks[0].Name] != deploy.RecordRecorded {
			t.Fatalf("recorded stack without the mark is not recorded: %+v", target.Resources)
		}
		if _, listed := records["other-installations"]; listed {
			t.Fatalf("another installation's stack is listed: %+v", target.Resources)
		}
		if _, listed := records["before-the-mark"]; listed {
			t.Fatalf("an unmarked stack is listed: %+v", target.Resources)
		}
		if records["ordinary-orphan"] != deploy.RecordUnrecorded {
			t.Fatalf("this installation's leftover is not reported: %+v", target.Resources)
		}
	}
	if !found {
		t.Fatal("first customer target missing")
	}
	for _, stack := range []string{"11", "12"} {
		name := map[string]string{"11": "other-installations", "12": "before-the-mark"}[stack]
		if code, answer := f.remove(t, name, stack); code != http.StatusConflict {
			t.Fatalf("remove %s: %d %+v", name, code, answer)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 0 {
		t.Fatalf("an unmarked stack reached provider DELETE: %v", f.deleted)
	}
}

// The inventory owner classifies age; a transport clock cannot overrule it.
func TestInfrastructureUsesInventoryOwnerUnknownDecision(t *testing.T) {
	t.Parallel()
	w := newTargetHTTPWorld(t)
	target := w.seedBuiltin(t)
	first := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	now := first
	owner := deployment.NewInventoryReadings(w.db, func() time.Time { return now })
	generation, err := owner.Start(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Finish(t.Context(), target, generation, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	w.infra.cache = owner
	for _, age := range []time.Duration{30 * time.Minute, 6*time.Hour + time.Second} {
		now = first.Add(age)
		w.infra.now = func() time.Time {
			if age > 6*time.Hour {
				return first
			}
			return first.Add(24 * time.Hour)
		}
		code, out := targetCall(t, w.infra.Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
		if code != http.StatusOK {
			t.Fatalf("inventory: %d %+v", code, out)
		}
		views := out["targets"].([]any)
		if len(views) != 1 {
			t.Fatalf("targets: %+v", views)
		}
		got, _ := views[0].(map[string]any)["unknown"].(bool)
		if got != (age > 6*time.Hour) {
			t.Errorf("transport overruled owner at age %v: %+v", age, views[0])
		}
	}
}
