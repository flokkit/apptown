package dbschema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

// Migration 231: not applicable with its reason counts as met in every yes
// given in advance, so no stored workflow version keeps the option that
// opted in to it, and the stock sentences written before not applicable
// existed say the rule AppTown applies. A current stock version stored with
// the option, and an older one with the old sentence, both become exactly
// the shipped file, so the stock sync cuts no new version. Other workflow
// text is left byte for byte.
//
// Kills: either statement removed, the option's line left behind or a
// neighbouring line eaten, the sentence left old, or other YAML rewritten.
func TestMigration231CountsNotApplicableAsMetInStoredWorkflows(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 231)]); err != nil {
		t.Fatal(err)
	}
	const optionLine = "            inAdvanceAdmitsNotApplicable: true\n"
	const newWords, oldWords = "with every check passing or not applicable, none excused.", "with every check passing, none excused."
	own := "apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: own-flow}\n# every check passing, none excused, said here in a comment\nspec: {kind: pipeline, stages: [{name: s, steps: [{name: a, uses: approval, with: {means: m}}]}]}\n"
	want := map[string]string{}
	stored := map[string]string{}
	for i, name := range []string{"land", "implement", "quick-change"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		shipped := string(body)
		at := strings.Index(shipped, newWords)
		if at < 0 {
			t.Fatalf("stock %s has no in-advance sentence", name)
		}
		end := at + strings.Index(shipped[at:], "\n") + 1
		// The current version as released, with the option after its sentence.
		withOption := shipped[:end] + optionLine + shipped[end:]
		// An older version: the sentence before not applicable existed.
		oldSentence := strings.Replace(shipped, newWords, oldWords, 1)
		for v, yaml := range []string{oldSentence, withOption} {
			key := fmt.Sprintf("%s v%d", name, v+1)
			stored[key], want[key] = yaml, shipped
			insertWorkflowVersion231(t, db, 100+i*10, name, v+1, yaml)
		}
	}
	stored["own-flow v1"], want["own-flow v1"] = own, own
	insertWorkflowVersion231(t, db, 200, "own-flow", 1, own)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 231)+1]); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT substr(l.name, 6) || ' v' || v.version, v.yaml FROM lib_workflow_versions v JOIN lib_workflows l ON l.id=v.lib_id WHERE v.id::text LIKE '00000000-0000-7000-8231-%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, yaml string
		if err := rows.Scan(&k, &yaml); err != nil {
			t.Fatal(err)
		}
		got[k] = yaml
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d versions, want %d", len(got), len(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s after migration differs from what it should be (stored %d bytes, got %d, want %d)", k, len(stored[k]), len(got[k]), len(w))
		}
		if _, err := schema.ParseWorkflowStored([]byte(got[k])); err != nil {
			t.Errorf("%s does not parse: %v", k, err)
		}
	}
}

// insertWorkflowVersion231 stores a workflow version as a release did, under
// a library name of its own (the release's rows may hold the stock names).
func insertWorkflowVersion231(t *testing.T, db *database.DB, n int, name string, version int, yaml string) {
	t.Helper()
	id := func(k int) string { return fmt.Sprintf("00000000-0000-7000-8231-%012d", k) }
	// sql-guarantee: workflow versions stored by a release whose language
	// still took the option; no operation of this build can publish one.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflows(id,name,origin,latest_version,updated_at) VALUES($1,$2,'stock',$3,now()) ON CONFLICT (id) DO UPDATE SET latest_version=EXCLUDED.latest_version`, id(n), "m231-"+name, version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES($1,$2,$3,$4,now(),'flokkit','')`, id(n+version), id(n), version, yaml); err != nil {
		t.Fatal(err)
	}
}
