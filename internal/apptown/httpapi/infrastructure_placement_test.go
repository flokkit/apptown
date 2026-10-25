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
	"github.com/flokkit/apptown/internal/apptown/jobs"
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
	target, err := w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: "customer-office", Kind: deployment.KindPortainer, Status: deployment.TargetReady, Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": "PT_KEY", "environment": "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/targets/customer-office/provider-association"
	values := map[string]string{"name": target.Name}
	code, preview := targetCall(t, w.targets.ProviderAssociation, "GET", path, "", values)
	if code != http.StatusOK {
		t.Fatalf("inspect: %d %+v", code, preview)
	}
	body, _ := json.Marshal(map[string]any{"expected": preview["scope"], "expectedRevision": preview["revision"], "resources": preview["resources"]})
	code, answer := targetCall(t, w.targets.AssociateProvider, "POST", path, string(body), values)
	if code != http.StatusOK {
		t.Fatalf("associate: %d %+v", code, answer)
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
			return []byte(sharedName + "\t\t" + stack + "\n" + orphanName + "\t\t\n"), nil
		case strings.HasPrefix(line, "volume rm"):
			removed++
			return nil, nil
		default:
			return nil, nil
		}
	}))
	observed := []deploy.Resource{{Kind: "volume", Name: sharedName, Stack: stack, Record: deploy.RecordUnrecorded, ObservedDaemonID: "engine-a"}}
	out, note, err := w.infra.attributeLeftovers(t.Context(), host, observed)
	if err != nil || len(out) != 0 || !strings.Contains(note, target.Name) || !strings.Contains(note, "shared-app/blue") {
		t.Fatalf("customer placement: %+v %q %v", out, note, err)
	}
	encoded, _ := json.Marshal(observed)
	if strings.Contains(string(encoded), "engine-a") {
		t.Fatal("private observation identity leaked into API")
	}
	merged := mergeInventory(nil, observed)
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

// This cache keeps the HTTP projection on the real provider inventory adapter.
type siblingInventoryCache struct{}

func (siblingInventoryCache) Live(ctx context.Context, _ *jobs.Queue, _ string, inv deploy.Inventorier, _ bool) ([]deploy.Resource, string, time.Time, bool) {
	resources, err := inv.Inventory(ctx)
	if err != nil {
		return nil, err.Error(), time.Time{}, false
	}
	return resources, "", time.Now(), false
}
func (siblingInventoryCache) Invalidate() {}

type siblingStack struct {
	ID         int64  `json:"Id"`
	Name       string `json:"Name"`
	EndpointID int64  `json:"EndpointId"`
}

type siblingInventoryWorld struct {
	w                              *targetHTTPWorld
	targets                        [2]*deployment.Target
	stacks                         []siblingStack
	mu                             sync.Mutex
	failList, failOwner, duplicate bool
	daemon                         string
	deleted                        []string
}

func newSiblingInventoryWorld(t *testing.T) *siblingInventoryWorld {
	t.Helper()
	f := &siblingInventoryWorld{w: newTargetHTTPWorld(t), daemon: "shared-engine"}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
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
		target, err := f.w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: name, Kind: deployment.KindPortainer, Status: deployment.TargetReady,
			Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": []string{"PT_A", "PT_B"}[i], "environment": "docker"}})
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/targets/" + name + "/provider-association"
		values := map[string]string{"name": name}
		code, preview := targetCall(t, f.w.targets.ProviderAssociation, "GET", path, "", values)
		if code != http.StatusOK {
			t.Fatalf("inspect %s: %d %+v", name, code, preview)
		}
		body, err := json.Marshal(map[string]any{"expected": preview["scope"], "expectedRevision": preview["revision"], "resources": preview["resources"]})
		if err != nil {
			t.Fatal(err)
		}
		code, answer := targetCall(t, f.w.targets.AssociateProvider, "POST", path, string(body), values)
		if code != http.StatusOK {
			t.Fatalf("associate %s: %d %+v", name, code, answer)
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
		f.stacks = append(f.stacks, siblingStack{ID: int64(i + 7), Name: stack, EndpointID: 3})
	}
	f.stacks = append(f.stacks, siblingStack{ID: 9, Name: "ordinary-orphan", EndpointID: 3})
	f.w.infra.cache = siblingInventoryCache{}
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
