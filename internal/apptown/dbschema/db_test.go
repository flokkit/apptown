package dbschema

import (
	"context"
	"os"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

var emptySuite = dbtest.NewSuite(func(_ context.Context, url string) (*database.DB, error) { return database.Open(url) })

func TestMain(m *testing.M) { os.Exit(emptySuite.Run(m)) }

// Migration inputs start empty; cloning the current schema only to drop it
// wastes catalog work and obscures which schema the test is actually exercising.
func emptyDB(t *testing.T) *database.DB { t.Helper(); return emptySuite.New(t) }
