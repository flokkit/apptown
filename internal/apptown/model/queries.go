package model

import (
	"github.com/flokkit/apptown/internal/apptown/model/internal/modelsql"
	"github.com/flokkit/apptown/internal/database"
)

// queries binds named model SQL to a pool or transaction. SQL() retains
// statement-fault injection for rollback and error-path tests.
func queries(q database.Queryer) *modelsql.Queries { return modelsql.New(q.SQL()) }
