package httpapi

import (
	"context"
	"os"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/stock"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }
func configurationFixture(t *testing.T) Configuration {
	t.Helper()
	catalog := dependency.NewCatalog(databaseSuite.New(t), nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	return NewConfiguration(catalog)
}
