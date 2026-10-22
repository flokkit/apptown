package dependency

import (
	"github.com/flokkit/apptown/internal/apptown/dependency/internal/dependencysql"
	"github.com/flokkit/apptown/internal/database"
)

// queries binds the package's named SQL to a pool or transaction. SQL() keeps
// the test-only statement fault attached to generated statements.
func queries(q database.Queryer) *dependencysql.Queries { return dependencysql.New(q.SQL()) }
