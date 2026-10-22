package secret

import (
	"github.com/flokkit/apptown/internal/apptown/secret/internal/secretsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// queries binds the package's named SQL (internal/secretsql/queries.sql) to a
// pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *secretsql.Queries { return secretsql.New(q.SQL()) }

// binding is a stored row in the words the subsystem speaks: generated is a
// flag here and a count in the column, and the wire keeps its own shape.
func binding(r secretsql.SecretBinding) Binding {
	return Binding{
		Deployment: r.Deployment, Name: r.Name, Store: r.Store, Ref: r.Ref,
		Version: r.Version, Generated: r.Generated != 0,
		UpdatedAt: stamp.Of(r.UpdatedAt).String(), UpdatedBy: r.UpdatedBy, UpdatedByPrincipal: r.UpdatedByPrincipal,
	}
}
