package dependency

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/stock"
)

// Every stock kind states its contract with the app (provides), and what
// each of its fulfilments injects is exactly that: a name injected but not
// provided would reach an app on one target only.
func TestEveryStockKindProvidesExactlyWhatItInjects(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(stock.FS, "dependencies/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("stock kinds: %v %v", files, err)
	}
	for _, f := range files {
		k := stockKind(t, strings.TrimSuffix(strings.TrimPrefix(f, "dependencies/"), ".yaml"))
		if len(k.Provides) == 0 {
			t.Errorf("%s states no contract (provides:)", k.Capability)
			continue
		}
		for name, meaning := range k.Provides {
			if strings.TrimSpace(meaning) == "" {
				t.Errorf("%s provides %s without saying what it means", k.Capability, name)
			}
		}
	}
}

// A kind whose inject differs from its contract is refused, both ways.
func TestAKindIsHeldToWhatItProvides(t *testing.T) {
	t.Parallel()
	base := "capability: kv\nlocal:\n  image: kv:1\n  inject:\n    KV_URL: kv://{host}\n"
	if _, err := Parse(base + "provides:\n  KV_URL: the address\n"); err != nil {
		t.Fatalf("a kind injecting what it provides: %v", err)
	}
	extra := base + "provides:\n  KV_HOST: the host\n"
	if _, err := Parse(extra); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), "injects KV_URL, which it does not provide") || !strings.Contains(err.Error(), "does not inject KV_HOST") {
		t.Fatalf("a kind injecting other names than it provides: %v", err)
	}
	register := "capability: dsn\nregister:\n  connector: x\n  inject:\n    DSN: ${dsn}\nprovides:\n  OTHER: no\n"
	if _, err := Parse(register); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("a registration held to the contract: %v", err)
	}
}

// The words a later form naming two needs of one kind would use stay free:
// no kind, stock or custom, may take them as a parameter.
func TestNoKindTakesAWordKeptForNamingNeeds(t *testing.T) {
	t.Parallel()
	base := "capability: kv\nlocal:\n  image: kv:1\n  inject:\n    KV_URL: kv://{host}\nprovides:\n  KV_URL: the address\n"
	for _, decl := range []string{"defaults:\n  prefix: KV_\n", "parameters: [as]\n", "parameters: [kind]\n"} {
		if _, err := Parse(base + decl); !errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), "a word kept for naming needs") {
			t.Fatalf("%q: %v", decl, err)
		}
	}
	if _, err := Parse(base + "parameters: [ttl]\n"); err != nil {
		t.Fatalf("an ordinary parameter: %v", err)
	}
}

// What the catalogue refuses in a contract's needs, the same wherever it is
// asked: an integration under needs:, a value AppTown owns, and two needs
// that give the app one variable. Two alternative kinds giving the same
// names may both be catalogued and each used alone.
func TestAContractsNeedsAreRefusedAsDeclared(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	clone := "capability: pg-alt\nlocal:\n  image: postgres:16-alpine\n  inject:\n    DATABASE_URL: postgres://{host}/app\nprovides:\n  DATABASE_URL: the connection URL\n"
	if _, err := c.Save(t.Context(), clone, "bls"); err != nil {
		t.Fatalf("an alternative giving the same name is catalogued: %v", err)
	}
	for _, tc := range []struct {
		name   string
		needs  []string
		params map[string]map[string]string
		want   string
	}{
		{name: "stock needs", needs: []string{"postgres", "redis", "s3", "llm", "mariadb"}},
		{name: "the alternative alone", needs: []string{"pg-alt"}},
		{name: "a parameter of its own", needs: []string{"postgres"}, params: map[string]map[string]string{"postgres": {"database": "ledger", "user": "keeper"}}},
		{name: "both alternatives", needs: []string{"postgres", "pg-alt"}, want: "DATABASE_URL is given by postgres and pg-alt"},
		{name: "a password", needs: []string{"postgres"}, params: map[string]map[string]string{"postgres": {"password": "sourdough"}}, want: "AppTown sets the password of the postgres need"},
		{name: "a mariadb password", needs: []string{"mariadb"}, params: map[string]map[string]string{"mariadb": {"password": "x"}}, want: "remove password from needs.mariadb"},
		{name: "the llm limit", needs: []string{"llm"}, params: map[string]map[string]string{"llm": {"limit": "5", "limitReset": "monthly"}}},
		{name: "disposable", needs: []string{"redis"}, params: map[string]map[string]string{"redis": {"disposable": "true"}}},
		// A parameter the kind does not take would do nothing today and
		// something once the kind learns it: refused, never ignored.
		{name: "a parameter the kind does not take", needs: []string{"postgres"}, params: map[string]map[string]string{"postgres": {"shared_buffers": "256MB"}}, want: "the postgres need does not take shared_buffers: remove shared_buffers from needs.postgres in .apptown/app.yaml (it takes database, disposable, user, version, size and resources)"},
		{name: "an s3 parameter", needs: []string{"s3"}, params: map[string]map[string]string{"s3": {"retention_days": "2555"}}, want: "the s3 need does not take retention_days"},
		{name: "a word kept for naming needs", needs: []string{"postgres"}, params: map[string]map[string]string{"postgres": {"kind": "postgres", "prefix": "REPORTS_"}}, want: "does not take kind or prefix"},
		{name: "error reporting", needs: []string{"errors"}, want: "errors is an integration, not a need"},
		// Withdrawn before 1.0 (sso and telemetry): attached integrations
		// give an app those values; the stock entries were never fulfilled.
		{name: "sign-in", needs: []string{"sso"}, want: "this app needs sso, which is not in the dependency catalog"},
		{name: "telemetry", needs: []string{"telemetry"}, want: "this app needs telemetry, which is not in the dependency catalog"},
		{name: "an unknown need", needs: []string{"postgres", "quantum-db"}, want: "this app needs quantum-db, which is not in the dependency catalog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, err := c.RefusesNeeds(t.Context(), tc.needs, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
				t.Fatalf("refusal %q, want %q", why, tc.want)
			}
		})
	}
}

// The stock catalogue's contracts give every variable once: no two stock
// needs can clash in one app.
func TestNoTwoStockNeedsGiveTheSameVariable(t *testing.T) {
	t.Parallel()
	files, _ := fs.Glob(stock.FS, "dependencies/*.yaml")
	seen := map[string]string{}
	for _, f := range files {
		k := stockKind(t, strings.TrimSuffix(strings.TrimPrefix(f, "dependencies/"), ".yaml"))
		for _, name := range k.Names() {
			if other, ok := seen[name]; ok {
				t.Errorf("%s is given by %s and %s", name, other, k.Capability)
			}
			seen[name] = k.Capability
		}
	}
	if !slices.Contains(sortedKeys(seen), "S3_BUCKET") {
		t.Fatalf("names: %v", sortedKeys(seen))
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// The guide's contract table is the catalogue's: every variable a stock
// need provides is named in the need's row, and nothing else is.
func TestTheGuidesContractTableIsTheCatalogues(t *testing.T) {
	t.Parallel()
	raw, err := fs.ReadFile(stock.FS, "docs/guides/services-and-data.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(raw)
	start := strings.Index(section, "## What each need gives the app")
	if start < 0 {
		t.Fatal("the guide has no contract section")
	}
	section = section[start:]
	if end := strings.Index(section[3:], "\n## "); end > 0 {
		section = section[:end+3]
	}
	rows := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| `") {
			cells := strings.Split(line, "|")
			rows[strings.Trim(strings.TrimSpace(cells[1]), "`")] = cells[2]
		}
	}
	files, _ := fs.Glob(stock.FS, "dependencies/*.yaml")
	for _, f := range files {
		k := stockKind(t, strings.TrimSuffix(strings.TrimPrefix(f, "dependencies/"), ".yaml"))
		row, ok := rows[k.Capability]
		if k.Integration {
			if ok {
				t.Errorf("%s is an integration and has a row in the needs contract", k.Capability)
			}
			continue
		}
		if !ok {
			t.Errorf("the guide has no contract row for %s", k.Capability)
			continue
		}
		var named []string
		for _, m := range regexpBackticked.FindAllStringSubmatch(row, -1) {
			if strings.ToUpper(m[1]) == m[1] {
				named = append(named, m[1])
			}
		}
		slices.Sort(named)
		if want := k.Names(); !slices.Equal(named, want) {
			t.Errorf("%s: the guide names %v, the catalogue provides %v", k.Capability, named, want)
		}
		delete(rows, k.Capability)
	}
	for need := range rows {
		t.Errorf("the guide has a row for %s, which the catalogue does not ship", need)
	}
}

var regexpBackticked = regexp.MustCompile("`([A-Z][A-Z0-9_]+)`")

// A need reference must name a variable its need documents, with the
// sentence saying what the need gives; a register-only need's values
// (llm) cannot be referenced yet; a documented variable passes.
//
// Kills: the provides check removed; the register-only refusal removed.
func TestNeedReferencesAreHeldToWhatTheNeedGives(t *testing.T) {
	c := NewCatalog(testDB(t), nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if why, err := c.RefusesNeedRefs(ctx, []appcontract.NeedRef{{Part: "web", Key: "HOST", Need: "postgres", Var: "PGHOST"}}); err != nil || why != "" {
		t.Fatalf("a documented variable: %q %v", why, err)
	}
	why, err := c.RefusesNeedRefs(ctx, []appcontract.NeedRef{{Part: "web", Key: "HOST", Need: "postgres", Var: "PGHOSTNAME"}})
	if err != nil || !strings.Contains(why, "PGHOSTNAME") || !strings.Contains(why, "PGHOST,") || !strings.Contains(why, "environment: HOST") {
		t.Fatalf("an undocumented variable: %q %v", why, err)
	}
	why, err = c.RefusesNeedRefs(ctx, []appcontract.NeedRef{{Part: "web", Key: "KEY", Need: "llm", Var: "LLM_API_KEY"}})
	if err != nil || !strings.Contains(why, "cannot be referenced yet") {
		t.Fatalf("a register-only need: %q %v", why, err)
	}
}
