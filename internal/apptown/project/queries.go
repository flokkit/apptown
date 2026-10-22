package project

import (
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
)

// queries binds generated statements to a pool or transaction, preserving test faults.
func queries(q database.Queryer) *projectsql.Queries { return projectsql.New(q.SQL()) }
