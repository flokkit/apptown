package conversation

import (
	"os"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/dbschema"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }
