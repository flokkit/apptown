package maintenance

import (
	"os"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/dbtest"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }
