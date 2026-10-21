package dependency

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/stock"
)

const redis = "capability: redis\ntitle: Redis\nlocal:\n  image: redis:7\n  port: 6379\n"
const sso = "capability: sso\ntitle: SSO\nregister:\n  connector: authentik\n"

// A kind needs a slug capability and one way to be fulfilled.
func TestParseRules(t *testing.T) {
	if _, err := Parse("capability: Bad Name\nlocal:\n  image: x\n"); !errors.Is(err, ErrSpec) {
		t.Fatalf("slug rule: %v", err)
	}
	if _, err := Parse("capability: nothing\ntitle: none\n"); !errors.Is(err, ErrSpec) {
		t.Fatalf("fulfilment rule: %v", err)
	}
	if _, err := Parse("capability: [\n"); !errors.Is(err, ErrSpec) {
		t.Fatalf("bad yaml: %v", err)
	}
	if d, err := Parse(sso); err != nil || d.Register == nil || d.Register.Connector != "authentik" {
		t.Fatalf("register-only kind: %v %+v", err, d)
	}
}

// A kind is experimental in data and says so first in its description,
// together: a badge and a paragraph that disagree would each mislead. An
// unknown status and a summary of more than one line are refused.
func TestAKindIsExperimentalInDataAndProseTogether(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]string{
		"status without the prose": redis + "status: experimental\ndescription: An in-memory cache.\n",
		"an unknown status":        redis + "status: beta\n",
		"a two-line summary":       redis + "summary: \"one\\ntwo\"\n",
	} {
		if _, err := Parse(spec); !errors.Is(err, ErrSpec) {
			t.Errorf("%s: want ErrSpec, got %v", name, err)
		}
	}
	d, err := Parse(redis + "status: experimental\ndescription: \"Experimental: no app has shipped with it yet.\"\nsummary: A cache.\n")
	if err != nil || d.Status != StatusExperimental || d.Summary != "A cache." {
		t.Fatalf("experimental kind: %v %+v", err, d)
	}
}

// A custom kind saved before status existed (a clone of a stock
// experimental kind: the prefix, no status, no summary) is still listed,
// still resolved for an app's need, and still experimental, so its tile
// keeps its badge and shows its description.
func TestASavedExperimentalKindWithoutStatusStaysReadableAndExperimental(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	ctx := context.Background()
	spec := "capability: cache-clone\ntitle: Cache\ndescription: \"Experimental: no app has shipped with it yet.\"\nlocal:\n  image: redis:7\n  port: 6379\n"
	if _, err := c.Save(ctx, spec, "sam"); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := c.ByCapability(ctx, "cache-clone")
	if err != nil || got.Status != StatusExperimental || got.Summary != "" {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	all, err := c.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range all {
		found = found || (k.Capability == "cache-clone" && k.Status == StatusExperimental)
	}
	if !found {
		t.Fatal("the saved kind is missing from the catalogue, or lost its status")
	}
}

// Every shipped kind has a one-line summary for the tile that offers it.
func TestEveryShippedKindHasASummary(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(stock.FS, "dependencies")
	if err != nil || len(entries) == 0 {
		t.Fatalf("stock kinds: %v", err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(stock.FS, "dependencies/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		d, err := Parse(string(b))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%s has no summary", e.Name())
		}
	}
}

// A kind's copy is declared once, under copy:, and can be witnessed: an
// export needs a verify (a copy nothing confirms came back) and, for a
// kind with local data, a restore (an export that could never be put back
// in place, stopped or not); a copy that fills a new place says its format, import and client together; a copy
// with no export is AppTown's file copy and says only its caveat. The
// old local.backup block is refused by name, never ignored: ignored, it
// would turn a database's copy into a tar of its live files.
func TestParseBackupRules(t *testing.T) {
	base := "capability: kv\nlocal:\n  image: kv:1\n  data: /data\n"
	const taken = "copy:\n  ext: f\n  export: cat /data/f\n  restore: cat > /data/f\n  verify: cmp -s - /data/f\n"
	for name, tc := range map[string]struct {
		spec string
		ok   bool
	}{
		"export, restore and verify":   {taken, true},
		"no copy: a file copy":         {"", true},
		"a file copy with a caveat":    {"copy:\n  caveat: no metadata\n", true},
		"a file copy with a command":   {"copy:\n  caveat: no metadata\n  verify: check\n", false},
		"export without restore":       {strings.Replace(taken, "  restore: cat > /data/f\n", "", 1), false},
		"export without verify":        {strings.Replace(taken, "  verify: cmp -s - /data/f\n", "", 1), false},
		"export without ext":           {strings.Replace(taken, "  ext: f\n", "", 1), false},
		"restore without export":       {strings.Replace(taken, "  export: cat /data/f\n", "", 1), false},
		"stopped restore":              {taken + "  restoreStopped: true\n", true},
		"stopped with no restore":      {strings.Replace(taken, "  restore: cat > /data/f\n", "", 1) + "  restoreStopped: true\n", false},
		"fills a new place":            {taken + "  format: kv-file\n  import: load\n  client: kv:1\n", true},
		"a format with no import":      {taken + "  format: kv-file\n  client: kv:1\n", false},
		"an import with no format":     {taken + "  import: load\n  client: kv:1\n", false},
		"the old local.backup":         {"  backup:\n    command: cat /data/f\n    restore: cat > /data/f\n", false},
		"the old local.backup, caveat": {"  backup:\n    caveat: no metadata\n", false},
		"a file copy run in a client":  {"copy:\n  caveat: no metadata\n  runIn: client\n", false},
	} {
		_, err := Parse(base + tc.spec)
		if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrSpec)) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "the old local.backup" && (err == nil || !strings.Contains(err.Error(), "copy:")) {
			t.Errorf("the refusal names where the copy moved: %v", err)
		}
	}
}

// Stock kinds sync from files and refresh on every boot; a custom kind is
// saved with a receipt, and a stock one is never overwritten by hand.
func TestCatalogStockAndCustom(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	fsys := fstest.MapFS{
		"dependencies/redis.yaml":  {Data: []byte(redis)},
		"dependencies/broken.yaml": {Data: []byte("capability: nope\n")},
		"dependencies/notes.txt":   {Data: []byte("ignored")},
	}
	synced, skipped, err := c.SyncStock(context.Background(), fsys, "dependencies")
	if !errors.Is(err, ErrStockUnreadable) || !strings.Contains(err.Error(), "broken.yaml") || len(synced) != 1 || synced[0] != "redis" || len(skipped) != 1 || !strings.HasPrefix(skipped[0], "broken.yaml: ") {
		t.Fatalf("sync: %v %v %v", err, synced, skipped)
	}
	if _, err := c.Save(context.Background(), redis, "bls"); !errors.Is(err, ErrStock) {
		t.Fatalf("saving over stock: %v", err)
	}
	if _, err := c.Save(context.Background(), "capability: x\n", "bls"); !errors.Is(err, ErrSpec) {
		t.Fatalf("saving a bad spec: %v", err)
	}
	if _, err := c.ByCapability(t.Context(), "mystery"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	d, err := c.Save(context.Background(), sso, "bls")
	if err != nil || d.Capability != "sso" {
		t.Fatalf("save custom: %v", err)
	}
	// A custom kind may be saved again; the stock one is refreshed by sync
	// only, and sync leaves the custom one alone.
	if _, err := c.Save(context.Background(), sso+"description: mine\n", "bls"); err != nil {
		t.Fatal(err)
	}
	fsys["dependencies/redis.yaml"] = &fstest.MapFile{Data: []byte(redis + "description: newer\n")}
	fsys["dependencies/sso.yaml"] = &fstest.MapFile{Data: []byte(sso + "description: vendor\n")}
	delete(fsys, "dependencies/broken.yaml")
	if _, _, err := c.SyncStock(context.Background(), fsys, "dependencies"); err != nil {
		t.Fatal(err)
	}
	all, err := c.All(context.Background())
	if err != nil || len(all) != 2 || all[0].Capability != "redis" || all[1].Capability != "sso" {
		t.Fatalf("all: %v %+v", err, all)
	}
	if all[0].Description != "newer" || all[1].Description != "mine" {
		t.Fatalf("sync must refresh stock and leave custom alone: %q %q", all[0].Description, all[1].Description)
	}
	receipts, err := audit.Recent(context.Background(), c.db, audit.Filter{KindPrefix: "dependency.saved", Subject: "sso", Limit: audit.MaxLimit})
	if err != nil || len(receipts) == audit.MaxLimit {
		t.Fatalf("receipts: %v %d", err, len(receipts))
	}
	// The ledger filter is a kind prefix; the receipt kind is exact.
	saved := 0
	for _, receipt := range receipts {
		if receipt.Kind == "dependency.saved" {
			saved++
		}
	}
	if saved != 2 {
		t.Fatalf("receipts: %d", saved)
	}
}

// A shipped kind that stops parsing used to be skipped while the catalog kept the previous release's definition,
// so a need's backups went on as before or, worse, its data was quietly
// left out. Now the sync names the file and why, and the stored kind is
// the unreadable file: reading it gives the reason, never the old one.
func TestAShippedKindThatDoesNotParseIsNamedAndNeverKeptStale(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	good := "capability: kv\nlocal:\n  image: kv:1\n  data: /data\ncopy:\n  ext: f\n  export: cat /data/f\n  restore: cat > /data/f\n  verify: cmp -s - /data/f\n"
	if _, _, err := c.SyncStock(context.Background(), fstest.MapFS{"dependencies/kv.yaml": {Data: []byte(good)}}, "dependencies"); err != nil {
		t.Fatal(err)
	}
	broken := "capability: kv\nlocal:\n  image: kv:1\n  data: /data\ncopy:\n  ext: f\n  export: cat /data/f\n  verify: cmp -s - /data/f\n"
	misnamed := "capability: other\nlocal:\n  image: o:1\n"
	_, skipped, err := c.SyncStock(context.Background(), fstest.MapFS{"dependencies/kv.yaml": {Data: []byte(broken)}, "dependencies/mis.yaml": {Data: []byte(misnamed)}}, "dependencies")
	if !errors.Is(err, ErrStockUnreadable) || len(skipped) != 2 || !strings.Contains(err.Error(), "kv.yaml: ") || !strings.Contains(err.Error(), "no restore") || !strings.Contains(err.Error(), "mis.yaml: ") {
		t.Fatalf("the sync names each file and why: %v %v", err, skipped)
	}
	if _, err := c.ByCapability(t.Context(), "kv"); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), "no restore") {
		t.Fatalf("the kind is the unreadable shipped file, not the previous one: %v", err)
	}
	if _, err := c.ByCapability(t.Context(), "other"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a misnamed file adds no kind under another name: %v", err)
	}
	// The misnamed definition, stored under its file's name, is not
	// served under that name either, nor listed as the other kind.
	if _, err := c.ByCapability(t.Context(), "mis"); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), `declares the capability "other"`) {
		t.Fatalf("the misnamed file's kind is unreadable: %v", err)
	}
	all, err := c.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range all {
		if k.Capability == "other" || k.Capability == "mis" {
			t.Fatalf("a rejected definition is listed: %+v", k)
		}
	}
}

// A stock kind a release withdraws (dynamodb, before 1.0) is no longer
// catalogued after the sync: kept, an app declaring it would go on being
// fulfilled from a definition the release no longer ships. A custom kind
// is the person's and stays.
func TestAStockKindTheReleaseNoLongerShipsIsRemoved(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	gone := "capability: gone\nlocal:\n  image: gone:1\n"
	if _, _, err := c.SyncStock(context.Background(), fstest.MapFS{"dependencies/redis.yaml": {Data: []byte(redis)}, "dependencies/gone.yaml": {Data: []byte(gone)}}, "dependencies"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Save(context.Background(), sso, "bls"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.SyncStock(context.Background(), fstest.MapFS{"dependencies/redis.yaml": {Data: []byte(redis)}}, "dependencies"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ByCapability(t.Context(), "gone"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a withdrawn stock kind is still catalogued: %v", err)
	}
	for _, kept := range []string{"redis", "sso"} {
		if _, err := c.ByCapability(t.Context(), kept); err != nil {
			t.Fatalf("%s: %v", kept, err)
		}
	}
}

// Every kind the release ships parses and is named by its file.
func TestEveryShippedKindParses(t *testing.T) {
	t.Parallel()
	if synced, skipped, err := NewCatalog(testDB(t), nil).SyncStock(context.Background(), stock.FS, "dependencies"); err != nil || len(skipped) != 0 || len(synced) == 0 {
		t.Fatalf("shipped kinds: %v skipped %v synced %v", err, skipped, synced)
	}
}

// A copy that fills a new place declares its format, import and client
// together, and one with no verify is a copy nothing ever confirms came
// back. A provider's reach and native protection are ones the engine
// implements; a declaration can name one, never invent one or grant itself
// another credential.
func TestParseCopyRules(t *testing.T) {
	const full = "copy:\n  format: pg-custom\n  ext: dump\n  client: pg:1\n  export: dump\n  import: load\n  verify: check\n  env: {PGHOST: \"{host}\", PGPASSWORD: \"{password}\"}\n  empty: none\n"
	const reach = "cloud:\n  digitalocean:\n    kind: managed-database\n    reach: {how: connect, from: named-host, credential: app-user}\n"
	base := "capability: db\nlocal:\n  image: db:1\n"
	for name, tc := range map[string]struct {
		spec string
		ok   bool
	}{
		"the whole copy":         {full, true},
		"a copy and a reach":     {full + reach, true},
		"no verify":              {strings.Replace(full, "  verify: check\n", "", 1), false},
		"no import":              {strings.Replace(full, "  import: load\n", "", 1), false},
		"no client":              {strings.Replace(full, "  client: pg:1\n", "", 1), false},
		"a format that is prose": {strings.Replace(full, "pg-custom", "Postgres dump", 1), false},
		"a reach with no copy":   {reach, false},
		"exec in the cloud":      {full + strings.Replace(reach, "how: connect", "how: exec", 1), false},
		"the admin's credential": {full + strings.Replace(reach, "app-user", "admin", 1), false},
		"reached as no kind":     {full + strings.Replace(reach, "kind: managed-database\n", "", 1), false},
		"DigitalOcean backups":   {full + reach + "    native: {mechanism: do-database-backups}\n", true},
		"a reach with no env":    {strings.Replace(full, "  env: {PGHOST: \"{host}\", PGPASSWORD: \"{password}\"}\n", "", 1) + reach, false},
		"an env naming a secret": {strings.Replace(full, "{password}", "{admin_password}", 1), false},
		"a reach with no empty":  {strings.Replace(full, "  empty: none\n", "", 1) + reach, false},
		"a reach, filling none":  {strings.NewReplacer("  format: pg-custom\n", "", "  client: pg:1\n", "", "  import: load\n", "").Replace(full) + reach, false},
		"an unknown native":      {full + reach + "    native: {mechanism: snapshots}\n", false},
		// A service whose image has no copy tool runs its copy in its
		// client image beside it; there is no other place.
		"run beside, in its client":  {full + "  runIn: client\n", true},
		"run in its own container":   {full + "  runIn: service\n", true},
		"run beside, with no client": {strings.NewReplacer("  format: pg-custom\n", "", "  client: pg:1\n", "", "  import: load\n", "").Replace(full) + "  runIn: client\n", false},
		"run somewhere else":         {full + "  runIn: host\n", false},
		// A bucket is reached as the app's key and takes no part in a
		// cluster's deletion, so it declares no empty check.
		"a bucket with no empty":    {strings.Replace(full, "  empty: none\n", "", 1) + strings.Replace(reach, "managed-database", "bucket", 1), true},
		"reached as another module": {full + strings.Replace(reach, "managed-database", "queue", 1), false},
	} {
		_, err := Parse(base + tc.spec)
		if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrSpec)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The stock Postgres declares its portable copy and how DigitalOcean's
// managed instance is reached and protected.
func TestStockPostgresDeclaresItsCopy(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	do := d.Cloud["digitalocean"]
	if d.Copy == nil || d.Copy.Format != "pg-custom" || do.Reach == nil || do.Reach.How != ReachConnect || do.Native == nil || do.Native.Mechanism != NativeDOBackups {
		t.Fatalf("postgres: copy %+v digitalocean %+v", d.Copy, do)
	}
}

// A kind's receipt carries the catalog's clock, however far it is from the
// machine's (no read shows a kind's update time, which the same clock
// stamps).
func TestAKindIsStampedByTheCatalogsClock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	at := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	c := NewCatalog(db, func() time.Time { return at })
	if _, _, err := c.SyncStock(context.Background(), fstest.MapFS{"dependencies/redis.yaml": {Data: []byte(redis)}}, "dependencies"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Save(context.Background(), sso, "bls"); err != nil {
		t.Fatal(err)
	}
	lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "dependency.saved", Limit: 1})
	if err != nil || len(lines) != 1 || lines[0].At != at.Format(time.RFC3339) {
		t.Fatalf("receipt %v %v, want it stamped %v", lines, err, at)
	}
}

// The stock MariaDB is a generic local need with an app-only connection and
// logical recovery; it must never silently fall back to copying live DB files.
func TestStockMariaDBDeclaresLocalConnectionAndLogicalCopy(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/mariadb.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	svc := d.ResolveLocal(map[string]string{"database": "journal", "user": "writer"}, "db", nil)
	if svc.Port != 3306 || svc.Inject["DB_NAME"] != "journal" || svc.Inject["DB_HOST"] != "db" || svc.Inject["DB_PASSWORD"] != "app" || svc.Inject["DB_SSL_MODE"] != "DISABLED" || svc.Env["MARIADB_ROOT_HOST"] != "localhost" {
		t.Fatalf("connection: %+v", svc)
	}
	if d.Copy == nil || d.Copy.Format != "mariadb-sql" || d.Copy.Export == "" || d.Copy.Verify == "" || d.Copy.Restore == "" || d.Copy.Env["MYSQL_PWD"] != "{password}" || len(d.Cloud) != 0 {
		t.Fatalf("logical local copy: %+v", d.Copy)
	}
}
