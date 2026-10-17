package project

import (
	"os"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/dbschema"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
