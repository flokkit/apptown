package dependency

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/stock"
)

func TestCatalogueSizeRequiresACompleteLocalConfiguration(t *testing.T) {
	for name, spec := range map[string]string{
		"unknown field": "small: {resources: {memory: 128M}, command: [run], image: surprise}",
		"bad resource":  "small: {resources: {cpu: 1}, command: [run]}",
		"unlimited":     "small: {resources: {}, command: [run]}",
		"bad name":      "Small: {resources: {memory: 128M}, command: [run]}",
		"empty command": "small: {resources: {memory: 128M}, command: []}",
		"empty arg":     "small: {resources: {memory: 128M}, command: ['']}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(redis + "sizes:\n  " + spec + "\n"); err == nil {
				t.Fatal("invalid size accepted")
			}
		})
	}
	if _, err := Parse(sso + "sizes: {small: {resources: {memory: 128M}, command: [run]}}\n"); err == nil {
		t.Fatal("register-only size accepted")
	}
}
func TestCatalogueResolvesSizesOnceAndLeavesDefaultsAlone(t *testing.T) {
	db := testDB(t)
	cat := NewCatalog(db, time.Now)
	if _, _, err := cat.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app: {web: {image: nginx, public: true}}\nneeds: {mariadb: {size: small}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cat.ResolveSizes(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if out.NeedResources["mariadb"].MemoryBytes != 128<<20 || !strings.Contains(strings.Join(out.NeedSizes["mariadb"].Command, " "), "--innodb-buffer-pool-size=32M") {
		t.Fatalf("wrong resolved preset: %+v", out)
	}
	if len(c.NeedResources) != 0 || len(c.NeedSizes["mariadb"].Command) != 0 {
		t.Fatal("resolution changed source")
	}
	// A captured generation carries its own command and cap, not today's lookup.
	saved := out.NeedSizes["mariadb"]
	saved.Name = "retired"
	saved.Command = []string{"--captured"}
	out.NeedSizes["mariadb"] = saved
	out.NeedResources["mariadb"] = appcontract.Resources{MemoryBytes: 192 << 20}
	replay, err := cat.ResolveSizes(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay, out) {
		t.Fatal("snapshot reinterpreted")
	}
	replay.NeedSizes["mariadb"].Command[0] = "changed"
	if out.NeedSizes["mariadb"].Command[0] != "--captured" {
		t.Fatal("snapshot argv aliased")
	}
	c.NeedSizes["mariadb"] = appcontract.NeedSize{Name: "unknown"}
	if _, err := cat.ResolveSizes(t.Context(), c); err == nil {
		t.Fatal("unknown size accepted")
	}
	kind := stockKind(t, "mariadb")
	memory, _ := kind.Resources.Bytes()
	if memory != 256<<20 || len(kind.ResolveLocal(nil, "", nil).Command) != 3 {
		t.Fatal("unsized defaults changed")
	}
}
