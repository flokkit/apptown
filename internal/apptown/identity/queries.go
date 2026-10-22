package identity

import (
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// queries binds the package's named SQL (internal/accesssql/queries.sql)
// to a pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *accesssql.Queries { return accesssql.New(q.SQL()) }
