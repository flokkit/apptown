package forge

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
)

func testApp(t *testing.T, configs ...Config) *App {
	t.Helper()
	return newApp(t, testDB(t), configs...)
}

// newApp builds an App on a fresh temp directory and drains its background
// owners before that directory is removed (cleanups
// run last-registered first, so this wait precedes TempDir's removal).
func newApp(t *testing.T, db *database.DB, configs ...Config) *App {
	t.Helper()
	cfg := Config{DataDir: t.TempDir()}
	if len(configs) != 0 {
		cfg = configs[0]
		if cfg.DataDir == "" {
			cfg.DataDir = t.TempDir()
		}
	}
	// A test forge is a developer's: its fixtures' script steps run on this
	// host (-host-scripts), as they always have.
	cfg.HostScripts = true
	a := constructApp(t, db, cfg)
	if err := a.runTok.Open(filepath.Join(a.cfg.dataDir, identity.RuntimeTokenKeyFile)); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.ShutdownBackground(context.Background()); err != nil {
			t.Errorf("stopping background owners: %v", err)
		}
	})
	return a
}

// restartApp explicitly models a dead process before resuming its work.
func restartApp(t *testing.T, db *database.DB) *App {
	t.Helper()
	a := newApp(t, db)
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	return a
}

// constructApp performs construction only, without stock or authorization writes.
func constructApp(t *testing.T, db *database.DB, cfg Config) *App {
	t.Helper()
	if cfg.DockerClient == nil {
		cfg.DockerClient = defaultTestDocker()
	}
	if cfg.SSHConfigPath == "" {
		// Each forge writes its attached hosts' stanzas into its own file,
		// never the user's ~/.ssh/config, so tests that reach a host run in
		// parallel.
		cfg.SSHConfigPath = filepath.Join(t.TempDir(), "ssh", "config")
	}
	a, err := New(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild overrides may capture the App and scenario state. Their lifetime
	// is this fixture's, including child subtests, not the whole test process.
	t.Cleanup(func() { workOverrides.Delete(a) })
	t.Cleanup(func() {
		holdRouteFields(t, a)
		// Neither record keeps the App alive past its test.
		builtRouteFields.Delete(a)
		routeTableBuilds.Delete(a)
	})
	return a
}

func testConfig(t *testing.T, cfg Config) configuration {
	t.Helper()
	c, err := cfg.prepare()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// now is the time a fixture writes into a stored text stamp: the real
// clock, RFC 3339, UTC.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// An App's route table is built once, with the owners the App holds then
// (routes.go). A test that replaces an owner after the table is built would
// be served by the old one, so every test App is held to the fields its
// table was built with: the cleanup constructApp registers fails a test
// that changed one afterwards. A test that needs a change after building
// rebuilds the table explicitly (rebuildRoutes).
var builtRouteFields sync.Map // *App -> map[string]string

// routeTableBuilds counts each App's table builds, for the test that
// holds Handler() to one.
var routeTableBuilds sync.Map // *App -> *atomic.Int32

func init() {
	routeTableBuilt = func(a *App) {
		builtRouteFields.Store(a, routeFields(a))
		n, _ := routeTableBuilds.LoadOrStore(a, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
	}
}

func routeBuilds(a *App) int32 {
	n, ok := routeTableBuilds.Load(a)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

// holdRouteFields fails t if a's table was built and a field it was built
// with has changed since.
func holdRouteFields(t interface {
	Helper()
	Errorf(string, ...any)
}, a *App) {
	t.Helper()
	v, ok := builtRouteFields.LoadAndDelete(a)
	if !ok {
		return
	}
	built := v.(map[string]string)
	for name, now := range routeFields(a) {
		if built[name] != now {
			t.Errorf("App.%s changed after its route table was built; set it before the first Handler() call, or rebuildRoutes after changing it", name)
		}
	}
}

// rebuildRoutes forgets a's table, so the next Handler() call builds one
// with the App's current owners.
func rebuildRoutes(a *App) {
	a.routes = routeTable{}
	builtRouteFields.Delete(a)
}

// routeFields is each App field's identity: what a pointer, func, channel
// or interface refers to, a scalar's value, and the same, one level down,
// for a struct-valued field's exported fields. Maps are the App's own
// caches, and contents behind a reference are the owner's: both change as
// the App runs.
func routeFields(a *App) map[string]string {
	out := map[string]string{}
	v := reflect.ValueOf(a).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "routes" {
			continue
		}
		fv := reflect.NewAt(f.Type, unsafe.Pointer(v.Field(i).UnsafeAddr())).Elem()
		if fv.Kind() == reflect.Struct && !syncType(f.Type) {
			for j := 0; j < f.Type.NumField(); j++ {
				sf := f.Type.Field(j)
				// A struct-valued field's own state (its caches, its
				// counters) is its business; what it was given is exported.
				if syncType(sf.Type) || !sf.IsExported() || sf.Type.Kind() == reflect.Map {
					continue
				}
				sv := reflect.NewAt(sf.Type, unsafe.Pointer(fv.Field(j).UnsafeAddr())).Elem()
				out[f.Name+"."+sf.Name] = identityOf(sv)
			}
			continue
		}
		// A map field is the App's own cache or table, filled as it runs.
		if syncType(f.Type) || f.Type.Kind() == reflect.Map {
			continue
		}
		out[f.Name] = identityOf(fv)
	}
	return out
}

func syncType(t reflect.Type) bool {
	return t.PkgPath() == "sync" || t.PkgPath() == "sync/atomic"
}

func identityOf(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return fmt.Sprintf("%x", v.Pointer())
	case reflect.Slice:
		return fmt.Sprintf("%x/%d", v.Pointer(), v.Len())
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		e := v.Elem()
		if k := e.Kind(); k == reflect.Pointer || k == reflect.Map || k == reflect.Func || k == reflect.Chan {
			return fmt.Sprintf("%s:%x", e.Type(), e.Pointer())
		}
		return fmt.Sprintf("%s:%v", e.Type(), e)
	case reflect.Struct:
		return fmt.Sprintf("%v", v.Type()) // a deeper struct is held by its owner's identity above
	default:
		return fmt.Sprintf("%v", v)
	}
}

// An App builds its route table once: every Handler() call serves the same
// one. A field changed after it is built is caught by the fixture's guard,
// and rebuildRoutes makes the next call build a table with the new field.
func TestAnAppBuildsItsRouteTableOnce(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.Handler()
	a.Handler()
	if n := routeBuilds(a); n != 1 {
		t.Fatalf("two Handler() calls built %d tables", n)
	}
	replaced := *a.Projects
	a.Projects = &replaced
	var caught guardRecorder
	holdRouteFields(&caught, a)
	if len(caught) != 1 || !strings.Contains(caught[0], "App.Projects changed after its route table was built") {
		t.Fatalf("a field changed after the table was built was not caught: %q", caught)
	}
	rebuildRoutes(a)
	a.Handler()
	if n := routeBuilds(a); n != 2 {
		t.Fatalf("rebuildRoutes did not make the next call build a table: %d builds", n)
	}
	holdRouteFields(t, a) // built with the field as it is now
}

type guardRecorder []string

func (*guardRecorder) Helper() {}
func (g *guardRecorder) Errorf(format string, args ...any) {
	*g = append(*g, fmt.Sprintf(format, args...))
}
