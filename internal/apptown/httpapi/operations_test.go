package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// An operation's page names its project, so a person sees "Shop" and can
// go to it instead of reading the ledger's subject key. The operation's
// own fields stay as the ledger encodes them. An operation outlives its
// project: once the project is gone the operation still reads, without
// one.
func TestOperationGetNamesItsProject(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	if err := jobs.Migrate(context.Background(), f.DB.Pool()); err != nil {
		t.Fatal(err)
	}
	ops := operations.New(f.DB, access.Checked{Authority: identity.Scoped(f.Access, "operations"), Projects: f.Projects}, f.Access, nil)
	ops.Register(operations.Kind{Name: "test.restore", Capability: access.CapDeploymentDeploy, Read: access.CapProjectRead,
		Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			return operations.Outcome{State: operations.Succeeded}
		}})
	admit := func(pid ids.ID, deployment string) *operations.Operation {
		t.Helper()
		op, err := database.Transact(f.Context, f.DB, func(tx *database.Tx) (*operations.Operation, error) {
			return ops.AdmitIn(f.Context, tx, operations.Spec{Kind: "test.restore", ProjectID: pid, Subject: "project:" + pid.String() + "/deployment:" + deployment,
				Authority: access.Principal{Kind: "user", Name: "bls", Via: "session", Credential: identity.HeaderCredential("bls")}})
		})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	type view struct {
		StatusLabel string            `json:"statusLabel"`
		Kind        string            `json:"kind"`
		Subject     string            `json:"subject"`
		Project     *OperationProject `json:"project"`
	}
	var getAs func(*operations.Operation, *project.Projects) view
	get := func(op *operations.Operation) view { return getAs(op, f.Projects) }
	getAs = func(op *operations.Operation, projects *project.Projects) view {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/operations/"+op.ID.String(), nil).WithContext(f.Context)
		r.SetPathValue("id", op.ID.String())
		w := httptest.NewRecorder()
		NewOperations(ops, projects).Get(w, r)
		if w.Code != 200 {
			t.Fatalf("get operation: %d %s", w.Code, w.Body.String())
		}
		var v view
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	v := get(admit(f.ProjectID, "main"))
	if v.Project == nil || v.Project.Slug != "shop" || v.Project.Name != "Shop" {
		t.Fatalf("the operation's project: %+v", v.Project)
	}
	if v.Kind != "test.restore" || v.StatusLabel == "" || v.Subject == "" {
		t.Fatalf("the operation's own fields: %+v", v)
	}

	// A caller who may read the operation but not its project gets the
	// operation without the project.
	if hidden := getAs(admit(f.ProjectID, "staging"), project.New(f.DB, refuseProjects{})); hidden.Project != nil || hidden.Kind != "test.restore" {
		t.Fatalf("an unreadable project's operation: %+v", hidden)
	}

	if gone := get(admit(idstest.N(999), "main")); gone.Project != nil || gone.Kind != "test.restore" {
		t.Fatalf("a gone project's operation: %+v", gone)
	}
}

// refuseProjects is a project reader's authority that refuses every read.
type refuseProjects struct{}

func (refuseProjects) Require(context.Context, access.Principal, access.Permission) (access.Principal, error) {
	return access.Principal{}, &access.Denied{Code: "test", Reason: "this caller may not read the project"}
}
