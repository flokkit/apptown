package model

import (
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/model/internal/modelsql"
)

// queries binds named model SQL to a pool or transaction. SQL() retains
// statement-fault injection for rollback and error-path tests.
func queries(q database.Queryer) *modelsql.Queries { return modelsql.New(q.SQL()) }
