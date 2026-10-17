package project

import (
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project/internal/projectsql"
)

// queries binds generated statements to a pool or transaction, preserving test faults.
func queries(q database.Queryer) *projectsql.Queries { return projectsql.New(q.SQL()) }
