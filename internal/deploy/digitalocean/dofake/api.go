// Package dofake supplies the DigitalOcean HTTP API fixture shared by driver
// tests and assembled AppTown deployment tests. It is not a deployment driver.
package dofake

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// API is an isolated in-memory account. The zero value is ready to serve.
// Mutations and observations are synchronized; Snapshot returns detached data.
type API struct {
	mu       sync.Mutex
	state    State
	failures map[Request]int
	// onDatabase is told of each database made or removed on a cluster
	// and of each cluster deleted, so a test can make them real.
	onDatabase func(op, cluster, name string) error
	// spaces is the account's object store (ServeSpaces): the keys the
	// keys API makes reach it, as DigitalOcean's reach Spaces.
	spaces SpacesStore
}

// SpacesStore is the S3 side of the account's Spaces: its buckets, and the
// keys that reach them (an s3fake.Store).
type SpacesStore interface {
	Buckets() []string
	Grant(access, bucket string)
	Revoke(access string)
}

// ServeSpaces makes the Spaces keys API grant its keys on store.
func (f *API) ServeSpaces(store SpacesStore) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spaces = store
}

type Request struct{ Method, Path string }
type State struct {
	// Tags are the tags made on the account (POST /v2/tags).
	Tags     []string
	Registry string
	// The registry's subscription tier and the repositories it includes;
	// unset is DigitalOcean's free starter tier, one repository.
	RegistryTier         string
	RegistryIncluded     int
	RegistryRepositories []string
	// RepositoryBytes is a repository's latest image, compressed, as
	// repositoriesV2 reports it with one tag; a repository without one
	// reports no latest manifest.
	RepositoryBytes map[string]int64
	// RepositoryManifests are the image digests each repository holds;
	// deleting one removes it, and the repository's name stays listed, as
	// DOCR keeps it until garbage collection.
	RepositoryManifests map[string][]string
	// GarbageCollections counts the garbage collections started.
	GarbageCollections int
	// Collection is the status of the registry's garbage collection
	// running now, "" when none is.
	Collection             string
	Databases, Apps, Specs []map[string]any
	// FrontDoor makes each app's live URL a path on this fake
	// (/apps/<name>), where it answers as the public service's image would:
	// ReleasePath answers the identity its spec gives that service
	// (APPTOWN_RELEASE), or 404 when FrontDoorStock says the image runs the
	// file server's stock configuration. Off, the live URL is a platform
	// host name nothing answers.
	FrontDoor, FrontDoorStock bool
	// NoURL makes the platform name no live URL for an app, as it does
	// before its ingress is assigned.
	NoURL                                                               bool
	DeploymentReads, DeploymentGeneration, ListRequests, DeleteRequests int
	Requests                                                            []Request
	// ClusterDBs are the databases each cluster holds, by cluster id; a
	// new cluster holds the platform's defaultdb. ClusterUsers are each
	// cluster's users and their passwords. NativeBackups are each
	// cluster's own backups. DeletedClusters are the clusters deleted.
	ClusterDBs      map[string][]string
	ClusterUsers    map[string]map[string]string
	NativeBackups   map[string][]map[string]any
	DeletedClusters []string
	ClustersMade    int
	// AppsMade counts the apps created; each is named app-<n>.
	AppsMade int
	// ClusterHost and ClusterPort, when set, are the connection every new
	// cluster answers with, as a test's real Postgres stands in for them.
	ClusterHost string
	ClusterPort int
	// SpacesKeys are the account's Spaces keys (name, access_key, grants),
	// without their secrets; SpacesKeysMade counts the keys made.
	SpacesKeys     []map[string]any
	SpacesKeysMade int
}

func clone[T any](in T) T {
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// page answers one page of a list the way DigitalOcean does: per_page items
// (20 when unset) starting at page (1-based), with links.pages.next as an
// absolute URL on the public API host while more remain.
func page(r *http.Request, key string, items []map[string]any) map[string]any {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 {
		per = 20
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n <= 0 {
		n = 1
	}
	start := min((n-1)*per, len(items))
	end := min(start+per, len(items))
	out := map[string]any{key: items[start:end], "meta": map[string]any{"total": len(items)}}
	if end < len(items) {
		next := url.URL{Scheme: "https", Host: "api.digitalocean.com", Path: r.URL.Path,
			RawQuery: url.Values{"page": {strconv.Itoa(n + 1)}, "per_page": {strconv.Itoa(per)}}.Encode()}
		out["links"] = map[string]any{"pages": map[string]any{"next": next.String()}}
	}
	return out
}

func (f *API) Snapshot() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.state)
}

// SeedRegistry gives the account a registry on tier, including that many
// repositories (0: no limit), holding repositories.
func (f *API) SeedRegistry(name, tier string, included int, repositories ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Registry, f.state.RegistryTier, f.state.RegistryIncluded = name, tier, included
	f.state.RegistryRepositories = append([]string(nil), repositories...)
}

// SeedCollection makes the registry collect garbage with status ("": none).
func (f *API) SeedCollection(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Collection = status
}

// SeedRepository puts a repository in the account's registry, holding one
// tag whose image is compressedBytes, as a copy into DOCR leaves it.
func (f *API) SeedRepository(name string, compressedBytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.RegistryRepositories = append(f.state.RegistryRepositories, name)
	if f.state.RepositoryBytes == nil {
		f.state.RepositoryBytes = map[string]int64{}
	}
	f.state.RepositoryBytes[name] = compressedBytes
	if f.state.RepositoryManifests == nil {
		f.state.RepositoryManifests = map[string][]string{}
	}
	f.state.RepositoryManifests[name] = append(f.state.RepositoryManifests[name], "sha256:"+name+"-latest")
}

// SeedManifests puts image digests in a repository, which it adds when the
// registry has none of that name.
func (f *API) SeedManifests(repository string, digests ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.state.RegistryRepositories, repository) {
		f.state.RegistryRepositories = append(f.state.RegistryRepositories, repository)
	}
	if f.state.RepositoryManifests == nil {
		f.state.RepositoryManifests = map[string][]string{}
	}
	f.state.RepositoryManifests[repository] = append(f.state.RepositoryManifests[repository], digests...)
}

// OnDatabase tells fn of each database made ("create") or removed
// ("drop") on a cluster, and each cluster deleted ("delete-cluster", name
// empty); an error from fn is the platform's refusal.
func (f *API) OnDatabase(fn func(op, cluster, name string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onDatabase = fn
}

// SeedNativeBackups gives a cluster its own backups, each taken at.
func (f *API) SeedNativeBackups(cluster string, at ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.NativeBackups == nil {
		f.state.NativeBackups = map[string][]map[string]any{}
	}
	for _, t := range at {
		f.state.NativeBackups[cluster] = append(f.state.NativeBackups[cluster], map[string]any{"created_at": t, "size_gigabytes": 0.03})
	}
}

// ServeClustersAt makes every cluster made from now on answer host:port
// as its connection.
func (f *API) ServeClustersAt(host string, port int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.ClusterHost, f.state.ClusterPort = host, port
}

// SetConnection points a cluster's connection at host:port with adminURI,
// as a test's real Postgres stands in for the cluster.
func (f *API) SetConnection(cluster, host string, port int, adminURI string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, db := range f.state.Databases {
		if db["id"] == cluster {
			db["connection"] = map[string]any{"uri": adminURI, "host": host, "port": port}
		}
	}
}

// SetDatabaseTags replaces a cluster's tags, as a cluster made before a
// tag was given would carry.
func (f *API) SetDatabaseTags(id string, tags ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, db := range f.state.Databases {
		if db["id"] == id {
			db["tags"] = tags
		}
	}
}

func (f *API) SeedDatabase(database map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Databases = append(f.state.Databases, clone(database))
}

// SeedApp selects provider state independently of AppTown's persisted placement.
func (f *API) SeedApp(id, active, pending string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app := map[string]any{"id": id, "active_deployment": map[string]any{"id": active}}
	if pending != "" {
		app["pending_deployment"] = map[string]any{"id": pending}
	}
	f.state.Apps = append(f.state.Apps, app)
}

// ServeFrontDoor answers each app's front door on this fake; stock makes
// the app's image run the file server's stock configuration, which answers
// no release identity.
func (f *API) ServeFrontDoor(stock bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.FrontDoor, f.state.FrontDoorStock = true, stock
}

// ServeNoURL makes the platform name no URL for the apps it deploys.
func (f *API) ServeNoURL() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.NoURL = true
}

// AdvanceDeployment models the provider accepting work before AppTown records it.
func (f *API) AdvanceDeployment(appID, deploymentID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, app := range f.state.Apps {
		if app["id"] == appID {
			f.state.DeploymentGeneration++
			app["active_deployment"] = map[string]any{"id": deploymentID, "phase": "ACTIVE"}
			return true
		}
	}
	return false
}
func (f *API) Fail(method, path string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures == nil {
		f.failures = make(map[Request]int)
	}
	f.failures[Request{method, path}] = status
}

// LoseAnswer makes the API apply every request to method and path and then
// drop the connection without answering: the effect happens and the
// caller cannot know it did. Fail with status 0 clears it.
func (f *API) LoseAnswer(method, path string) { f.Fail(method, path, lostAnswer) }

// lostAnswer is the failure status that applies a request and answers nothing.
const lostAnswer = -1

func (f *API) Handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	// App Platform's instance sizes: a slice of its list as the API
	// answers it, numbers as strings, one size marked for deprecation.
	mux.HandleFunc("GET /v2/apps/tiers/instance_sizes", func(w http.ResponseWriter, r *http.Request) {
		size := func(slug, cpus, mem, usd string, deprecated bool) map[string]any {
			out := map[string]any{"slug": slug, "cpus": cpus, "memory_bytes": mem, "usd_per_month": usd}
			if deprecated {
				out["deprecation_intent"] = true
			}
			return out
		}
		j(w, 200, map[string]any{"instance_sizes": []map[string]any{
			size("basic-xxs", "1", "536870912", "5.00", true),
			size("apps-s-1vcpu-0.5gb", "1", "536870912", "5.00", false),
			size("apps-s-1vcpu-1gb-fixed", "1", "1073741824", "10.00", false),
			size("apps-s-1vcpu-2gb", "1", "2147483648", "25.00", false),
			size("apps-s-2vcpu-4gb", "2", "4294967296", "50.00", false),
			size("apps-d-2vcpu-8gb", "2", "8589934592", "98.00", false),
		}})
	})
	mux.HandleFunc("GET /v2/account", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, map[string]any{"account": map[string]any{"email": "operator@example.com", "status": "active", "team": map[string]any{"name": "Test"}}})
	})
	mux.HandleFunc("GET /v2/registry", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.state.Registry == "" {
			j(w, 404, map[string]any{"id": "not_found", "message": "registry does not exist"})
			return
		}
		j(w, 200, map[string]any{"registry": map[string]any{"name": f.state.Registry}})
	})
	mux.HandleFunc("GET /v2/registry/subscription", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tier, included := f.state.RegistryTier, f.state.RegistryIncluded
		if tier == "" {
			tier, included = "starter", 1
		}
		j(w, 200, map[string]any{"subscription": map[string]any{"tier": map[string]any{"slug": tier, "name": tier, "included_repositories": included}}})
	})
	mux.HandleFunc("GET /v2/registry/{name}/repositoriesV2", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.state.Registry == "" || r.PathValue("name") != f.state.Registry {
			j(w, 404, map[string]any{"id": "not_found", "message": "registry does not exist"})
			return
		}
		repos := []map[string]any{}
		for _, name := range f.state.RegistryRepositories {
			repo := map[string]any{"registry_name": f.state.Registry, "name": name}
			if size, ok := f.state.RepositoryBytes[name]; ok {
				repo["tag_count"], repo["manifest_count"] = 1, 1
				repo["latest_manifest"] = map[string]any{"repository": name, "compressed_size_bytes": size, "size_bytes": size * 2}
			}
			repos = append(repos, repo)
		}
		j(w, 200, page(r, "repositories", repos))
	})
	mux.HandleFunc("GET /v2/registry/{name}/repositories/{repo}/digests", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !slices.Contains(f.state.RegistryRepositories, r.PathValue("repo")) {
			j(w, 404, map[string]any{"id": "not_found", "message": "repository does not exist"})
			return
		}
		items := []map[string]any{}
		for _, d := range f.state.RepositoryManifests[r.PathValue("repo")] {
			items = append(items, map[string]any{"digest": d, "registry_name": r.PathValue("name"), "repository": r.PathValue("repo")})
		}
		j(w, 200, page(r, "manifests", items))
	})
	mux.HandleFunc("DELETE /v2/registry/{name}/repositories/{repo}/digests/{digest}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		repo, digest := r.PathValue("repo"), r.PathValue("digest")
		i := slices.Index(f.state.RepositoryManifests[repo], digest)
		if i < 0 {
			j(w, 404, map[string]any{"id": "not_found", "message": "manifest does not exist"})
			return
		}
		f.state.RepositoryManifests[repo] = slices.Delete(f.state.RepositoryManifests[repo], i, i+1)
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v2/registry/{name}/garbage-collection", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.state.GarbageCollections++
		j(w, 201, map[string]any{"garbage_collection": map[string]any{"status": "requested"}})
	})
	// The running garbage collection, as DOCR answers it: 404 when none is.
	mux.HandleFunc("GET /v2/registry/{name}/garbage-collection", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.state.Collection == "" {
			j(w, 404, map[string]any{"id": "not_found", "message": "no active garbage collection"})
			return
		}
		j(w, 200, map[string]any{"garbage_collection": map[string]any{"uuid": "gc-1", "registry_name": r.PathValue("name"), "status": f.state.Collection, "created_at": "2026-10-08T04:45:54Z"}})
	})
	mux.HandleFunc("POST /v2/registry", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.state.Registry, _ = in["name"].(string)
		j(w, 201, map[string]any{"registry": map[string]any{"name": f.state.Registry}})
	})
	// Tags: a tag is made once, then assigned to resources; only database
	// clusters are tagged here.
	mux.HandleFunc("POST /v2/tags", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			j(w, 422, map[string]any{"id": "unprocessable_entity", "message": "a tag needs a name"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !slices.Contains(f.state.Tags, in.Name) {
			f.state.Tags = append(f.state.Tags, in.Name)
		}
		j(w, 201, map[string]any{"tag": map[string]any{"name": in.Name}})
	})
	mux.HandleFunc("POST /v2/tags/{tag}/resources", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Resources []struct {
				ID   string `json:"resource_id"`
				Type string `json:"resource_type"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			j(w, 400, map[string]any{"id": "bad_request", "message": err.Error()})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		tag := r.PathValue("tag")
		if !slices.Contains(f.state.Tags, tag) {
			j(w, 404, map[string]any{"id": "not_found", "message": "no such tag"})
			return
		}
		for _, res := range in.Resources {
			if res.Type != "database" {
				j(w, 422, map[string]any{"id": "unprocessable_entity", "message": "unsupported resource type"})
				return
			}
			for _, db := range f.state.Databases {
				if db["id"] != res.ID {
					continue
				}
				var tags []any
				switch have := db["tags"].(type) {
				case []any:
					tags = have
				case []string:
					for _, t := range have {
						tags = append(tags, t)
					}
				}
				if !slices.Contains(tags, any(tag)) {
					db["tags"] = append(tags, tag)
				}
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /v2/databases", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		j(w, 200, page(r, "databases", f.state.Databases))
	})
	mux.HandleFunc("POST /v2/databases", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		// Cluster ids are db-1, db-2, … in the order they are made.
		f.mu.Lock()
		f.state.ClustersMade++
		id := "db-" + strconv.Itoa(f.state.ClustersMade)
		f.mu.Unlock()
		db := map[string]any{"id": id, "name": in["name"], "engine": "pg", "version": in["version"], "region": in["region"], "size": in["size"], "status": "creating", "tags": in["tags"],
			"connection": map[string]any{"uri": "postgresql://doadmin:x@" + id + ".fake:25060/defaultdb", "host": id + ".fake", "port": 25060}}
		f.mu.Lock()
		if f.state.ClusterHost != "" {
			db["connection"] = map[string]any{"uri": "postgresql://doadmin:x@" + f.state.ClusterHost + "/defaultdb", "host": f.state.ClusterHost, "port": f.state.ClusterPort}
		}
		f.mu.Unlock()
		f.mu.Lock()
		f.state.Databases = append(f.state.Databases, db)
		f.mu.Unlock()
		j(w, 201, map[string]any{"database": db})
	})
	mux.HandleFunc("GET /v2/databases/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, db := range f.state.Databases {
			if db["id"] == r.PathValue("id") {
				db["status"] = "online" // the second look finds it up
				j(w, 200, map[string]any{"database": db})
				return
			}
		}
		j(w, 404, map[string]any{"id": "not_found"})
	})
	// hook reports a database operation to the test, outside the lock.
	hook := func(op, cluster, name string) error {
		f.mu.Lock()
		fn := f.onDatabase
		f.mu.Unlock()
		if fn == nil {
			return nil
		}
		return fn(op, cluster, name)
	}
	known := func(cluster string) bool {
		for _, db := range f.state.Databases {
			if db["id"] == cluster {
				return true
			}
		}
		return false
	}
	dbsOf := func(cluster string) []string {
		if f.state.ClusterDBs == nil {
			f.state.ClusterDBs = map[string][]string{}
		}
		if _, ok := f.state.ClusterDBs[cluster]; !ok {
			f.state.ClusterDBs[cluster] = []string{"defaultdb"}
		}
		return f.state.ClusterDBs[cluster]
	}
	mux.HandleFunc("POST /v2/databases/{id}/dbs", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		cluster := r.PathValue("id")
		if !dbName.MatchString(in.Name) {
			j(w, 422, map[string]any{"id": "unprocessable_entity", "message": in.Name + " is an invalid value for name. Constraints for this param are: must consist of alpha-numeric characters, underscores or dashes, may not start with dash, max 40 characters"})
			return
		}
		f.mu.Lock()
		exists := slices.Contains(dbsOf(cluster), in.Name)
		f.mu.Unlock()
		if exists {
			j(w, 422, map[string]any{"id": "unprocessable_entity", "message": "database " + in.Name + " already exists"})
			return
		}
		if err := hook("create", cluster, in.Name); err != nil {
			j(w, 500, map[string]any{"id": "server_error", "message": err.Error()})
			return
		}
		f.mu.Lock()
		f.state.ClusterDBs[cluster] = append(dbsOf(cluster), in.Name)
		f.mu.Unlock()
		j(w, 201, map[string]any{"db": map[string]any{"name": in.Name}})
	})
	mux.HandleFunc("GET /v2/databases/{id}/dbs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !known(r.PathValue("id")) {
			j(w, 404, map[string]any{"id": "not_found"})
			return
		}
		items := []map[string]any{}
		for _, n := range dbsOf(r.PathValue("id")) {
			items = append(items, map[string]any{"name": n})
		}
		j(w, 200, map[string]any{"dbs": items})
	})
	mux.HandleFunc("DELETE /v2/databases/{id}/dbs/{name}", func(w http.ResponseWriter, r *http.Request) {
		cluster, name := r.PathValue("id"), r.PathValue("name")
		f.mu.Lock()
		i := slices.Index(dbsOf(cluster), name)
		f.mu.Unlock()
		if i < 0 {
			j(w, 404, map[string]any{"id": "not_found"})
			return
		}
		if err := hook("drop", cluster, name); err != nil {
			j(w, 500, map[string]any{"id": "server_error", "message": err.Error()})
			return
		}
		f.mu.Lock()
		f.state.ClusterDBs[cluster] = slices.DeleteFunc(dbsOf(cluster), func(n string) bool { return n == name })
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v2/databases/{id}/users", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.state.ClusterUsers == nil {
			f.state.ClusterUsers = map[string]map[string]string{}
		}
		users := f.state.ClusterUsers[r.PathValue("id")]
		if users == nil {
			users = map[string]string{}
			f.state.ClusterUsers[r.PathValue("id")] = users
		}
		if _, ok := users[in.Name]; !ok {
			users[in.Name] = "pw-" + in.Name
		}
		j(w, 201, map[string]any{"user": map[string]any{"name": in.Name}})
	})
	mux.HandleFunc("GET /v2/databases/{id}/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		pw, ok := f.state.ClusterUsers[r.PathValue("id")][r.PathValue("name")]
		if !ok {
			j(w, 404, map[string]any{"id": "not_found", "message": "user not found"})
			return
		}
		j(w, 200, map[string]any{"user": map[string]any{"name": r.PathValue("name"), "role": "normal", "password": pw}})
	})
	mux.HandleFunc("GET /v2/databases/{id}/backups", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !known(r.PathValue("id")) {
			j(w, 404, map[string]any{"id": "not_found"})
			return
		}
		backups := f.state.NativeBackups[r.PathValue("id")]
		if backups == nil {
			backups = []map[string]any{}
		}
		j(w, 200, map[string]any{"backups": backups})
	})
	mux.HandleFunc("DELETE /v2/databases/{id}", func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("id")
		f.mu.Lock()
		there := known(cluster)
		f.mu.Unlock()
		if !there {
			j(w, 404, map[string]any{"id": "not_found"})
			return
		}
		if err := hook("delete-cluster", cluster, ""); err != nil {
			j(w, 500, map[string]any{"id": "server_error", "message": err.Error()})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.state.Databases = slices.DeleteFunc(f.state.Databases, func(db map[string]any) bool { return db["id"] == cluster })
		delete(f.state.ClusterDBs, cluster)
		delete(f.state.NativeBackups, cluster)
		f.state.DeletedClusters = append(f.state.DeletedClusters, cluster)
		w.WriteHeader(204)
	})
	appOut := func(app map[string]any) map[string]any {
		return map[string]any{"app": app}
	}
	mux.HandleFunc("GET /v2/apps", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.state.ListRequests++
		j(w, 200, page(r, "apps", f.state.Apps))
	})
	newDeployment := func(r *http.Request, app map[string]any) {
		f.state.DeploymentGeneration++
		dep := map[string]any{"id": "dep-" + strconv.Itoa(f.state.DeploymentGeneration), "phase": "ACTIVE"}
		app["pending_deployment"] = dep // the response names the deployment this spec caused
		app["active_deployment"] = dep
		name := app["spec"].(map[string]any)["name"].(string)
		app["live_url"] = "https://" + name + "-abc.ondigitalocean.app"
		if f.state.FrontDoor {
			app["live_url"] = "http://" + r.Host + "/apps/" + name
		}
		if f.state.NoURL {
			delete(app, "live_url")
		}
	}
	// The front door routes to the ingress rule's component, as App
	// Platform does: only that service's APPTOWN_RELEASE answers.
	mux.HandleFunc("GET /apps/{name}/_apptown/release", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, app := range f.state.Apps {
			spec, _ := app["spec"].(map[string]any)
			if spec["name"] != r.PathValue("name") || f.state.FrontDoorStock {
				continue
			}
			public := ""
			if ingress, _ := spec["ingress"].(map[string]any); ingress != nil {
				if rules, _ := ingress["rules"].([]any); len(rules) > 0 {
					comp, _ := rules[0].(map[string]any)["component"].(map[string]any)
					public, _ = comp["name"].(string)
				}
			}
			services, _ := spec["services"].([]any)
			for _, s := range services {
				if s.(map[string]any)["name"] != public {
					continue
				}
				envs, _ := s.(map[string]any)["envs"].([]any)
				for _, e := range envs {
					if env, _ := e.(map[string]any); env["key"] == "APPTOWN_RELEASE" {
						_, _ = io.WriteString(w, env["value"].(string))
						return
					}
				}
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /v2/apps", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec map[string]any `json:"spec"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if msg := secondVersionOfARepository(in.Spec); msg != "" {
			j(w, 400, map[string]any{"id": "invalid_argument", "message": msg})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		// Each app has its own id; the first is app-1.
		f.state.AppsMade++
		app := map[string]any{"id": "app-" + strconv.Itoa(f.state.AppsMade), "spec": in.Spec}
		newDeployment(r, app)
		f.state.Apps = append(f.state.Apps, app)
		f.state.Specs = append(f.state.Specs, in.Spec)
		j(w, 201, appOut(app))
	})
	mux.HandleFunc("PUT /v2/apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec map[string]any `json:"spec"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, app := range f.state.Apps {
			if app["id"] == r.PathValue("id") {
				app["spec"] = in.Spec
				newDeployment(r, app)
				f.state.Specs = append(f.state.Specs, in.Spec)
				j(w, 200, appOut(app))
				return
			}
		}
		j(w, 404, map[string]any{"id": "not_found"})
	})
	mux.HandleFunc("GET /v2/apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, app := range f.state.Apps {
			if app["id"] == r.PathValue("id") {
				j(w, 200, appOut(app))
				return
			}
		}
		j(w, 404, map[string]any{"id": "not_found"})
	})
	mux.HandleFunc("GET /v2/apps/{id}/deployments/{dep}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.state.DeploymentReads++
		f.mu.Unlock()
		j(w, 200, map[string]any{"deployment": map[string]any{"id": r.PathValue("dep"), "phase": "ACTIVE"}})
	})
	// The Spaces keys API: a grant on a bucket the store does not hold is
	// refused, as DigitalOcean refuses it ("invalid grant").
	mux.HandleFunc("POST /v2/spaces/keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name   string           `json:"name"`
			Grants []map[string]any `json:"grants"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			j(w, 400, map[string]any{"id": "bad_request", "message": err.Error()})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.spaces == nil || len(in.Grants) != 1 {
			j(w, 400, map[string]any{"id": "bad_request", "message": "a key has one grant here"})
			return
		}
		bucket, _ := in.Grants[0]["bucket"].(string)
		if perm, _ := in.Grants[0]["permission"].(string); (bucket == "") != (perm == "fullaccess") || (bucket != "" && !slices.Contains(f.spaces.Buckets(), bucket)) {
			j(w, 403, map[string]any{"id": "forbidden", "message": "invalid grant"})
			return
		}
		f.state.SpacesKeysMade++
		access := "DOFAKE" + strconv.Itoa(f.state.SpacesKeysMade)
		key := map[string]any{"name": in.Name, "access_key": access, "grants": in.Grants}
		f.state.SpacesKeys = append(f.state.SpacesKeys, key)
		f.spaces.Grant(access, bucket)
		made := clone(key)
		made["secret_key"] = "secret-" + access
		j(w, 201, map[string]any{"key": made})
	})
	mux.HandleFunc("GET /v2/spaces/keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		j(w, 200, page(r, "keys", f.state.SpacesKeys))
	})
	mux.HandleFunc("DELETE /v2/spaces/keys/{access}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		before := len(f.state.SpacesKeys)
		f.state.SpacesKeys = slices.DeleteFunc(f.state.SpacesKeys, func(k map[string]any) bool { return k["access_key"] == r.PathValue("access") })
		if len(f.state.SpacesKeys) == before {
			j(w, 404, map[string]any{"id": "not_found", "message": "no such key"})
			return
		}
		if f.spaces != nil {
			f.spaces.Revoke(r.PathValue("access"))
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("DELETE /v2/apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.state.Apps = slices.DeleteFunc(f.state.Apps, func(app map[string]any) bool { return app["id"] == r.PathValue("id") })
		w.WriteHeader(204)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.state.Requests = append(f.state.Requests, Request{r.Method, r.URL.Path})
		status := f.failures[Request{r.Method, r.URL.Path}]
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/apps/") {
			f.state.DeleteRequests++
		}
		f.mu.Unlock()
		if status == lostAnswer {
			mux.ServeHTTP(httptest.NewRecorder(), r)
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
		if status != 0 {
			j(w, status, map[string]any{"id": "injected", "message": "injected API failure"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// secondVersionOfARepository is App Platform's refusal at create of a spec
// whose components name two versions of one DOCR repository, word for word
// as it answered the restore test (RT-L3); its propose endpoint accepts
// such a spec. Direct creates confirmed it: two digests of one repository
// were refused with and without the images naming their registry, and the
// same digests from two repositories were created and went active. Only
// create was observed (not update), and only digests; a tag is taken as a
// version too. "" when the spec names one version per repository.
func secondVersionOfARepository(spec map[string]any) string {
	seen := map[string]string{}
	for _, kind := range []string{"services", "workers", "jobs"} {
		cs, _ := spec[kind].([]any)
		for _, c := range cs {
			img, _ := c.(map[string]any)["image"].(map[string]any)
			if img == nil || img["registry_type"] != "DOCR" {
				continue
			}
			repo, _ := img["repository"].(string)
			version, _ := img["digest"].(string)
			if version == "" {
				version, _ = img["tag"].(string)
			}
			if first, ok := seen[repo]; ok && first != version {
				return "App spec references an image tag or digest that is not configured for this app; the repository is known but this specific version is not"
			}
			seen[repo] = version
		}
	}
	return ""
}

// dbName is DigitalOcean's rule for a database's name, as its 422 states it.
var dbName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,39}$`)
