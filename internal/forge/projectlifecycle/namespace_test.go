package projectlifecycle

import (
	"os"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/dbschema"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

type consoleAdmissionFunc func(string, string) (func(), error)

func (f consoleAdmissionFunc) Block(key, value string) (func(), error) { return f(key, value) }
