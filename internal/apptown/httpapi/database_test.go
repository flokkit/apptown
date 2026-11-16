package httpapi

import (
	"context"
	"os"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/stock"
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
