package complaint

import (
	"os"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
