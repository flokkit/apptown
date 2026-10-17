package dependency

import (
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/dependency/internal/dependencysql"
)

// queries binds the package's named SQL to a pool or transaction. SQL() keeps
// the test-only statement fault attached to generated statements.
func queries(q database.Queryer) *dependencysql.Queries { return dependencysql.New(q.SQL()) }
