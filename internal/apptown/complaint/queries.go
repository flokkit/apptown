package complaint

import (
	"github.com/flokkit/apptown/internal/apptown/complaint/internal/complaintsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// queries binds the package's named SQL (internal/complaintsql/queries.sql)
// to a pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *complaintsql.Queries { return complaintsql.New(q.SQL()) }

// record is the stored row as the box speaks of it. The project's slug and
// name come from the page's report; the state is derived, never stored.
func record(r complaintsql.Complaint, slug, name string) Complaint {
	c := Complaint{
		ID: r.ID, Number: r.Number, At: stamp.Of(r.At).String(), Source: r.Source, RunID: r.RunID,
		Workflow: r.Workflow, Step: r.Step, SessionID: r.SessionID, Actor: r.Actor, ActorPrincipal: r.ActorPrincipal,
		Text: r.Text, ChangeID: r.ChangeID, Why: r.Why, Wish: r.Wish, Seen: r.Seen,
		Answer: r.Answer, AnsweredBy: r.AnsweredBy, AnsweredByPrincipal: r.AnsweredByPrincipal,
		AnsweredAt: stamp.Time{NullTime: r.AnsweredAt}, ArchivedAt: stamp.Time{NullTime: r.ArchivedAt},
		Project: slug, ProjectName: name, About: r.About, PackVersion: r.PackVersion,
	}
	c.State = StateOf(c.Answer, c.ArchivedAt.Valid).String()
	return c
}
