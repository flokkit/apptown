package identity

import (
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
)

// queries binds the package's named SQL (internal/accesssql/queries.sql)
// to a pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *accesssql.Queries { return accesssql.New(q.SQL()) }
