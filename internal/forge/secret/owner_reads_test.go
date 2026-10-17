package secret

import (
	"context"
	"strconv"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/secret/internal/secretsql"
	"flokkit.io/forge/internal/ids"
)

// Test observations through the owners' own reads, so a renamed column breaks
// compilation instead of silently changing what an assertion means.

// ledger is every receipt of exactly one kind, read through the audit owner.
// The read is bounded, so it refuses to answer at the bound.
func ledger(t *testing.T, db *database.DB, kind string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	var out []audit.Entry
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// storedVersions counts the versions of one stored value. Versions are minted
// from 1 without gaps, and the tests here write fewer than ten, so the probe
// is complete.
func storedVersions(t *testing.T, db *database.DB, scope, name string) int {
	t.Helper()
	n := 0
	for v := 1; v <= 10; v++ {
		ok, err := queries(db).SecretValueVersionExists(context.Background(), secretsql.SecretValueVersionExistsParams{Scope: scope, Name: name, Column3: strconv.Itoa(v)})
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			n++
		}
	}
	return n
}

// deploymentBindings is every binding recorded for the project's deployment.
func deploymentBindings(t *testing.T, db *database.DB, projectID ids.ID, deployment string) []secretsql.SecretBinding {
	t.Helper()
	rows, err := queries(db).BindingsForDeployment(context.Background(), secretsql.BindingsForDeploymentParams{ProjectID: projectID, Deployment: deployment})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func wantCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: %d want %d", what, got, want)
	}
}
