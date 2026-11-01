package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The HTTP handler's guarantee is the translation of the conformance owner's
// command outcomes into statuses. This source and authority keep the fixture
// at that boundary: the owner still creates and persists the real reviews and
// verdicts, but no fleet repository, model, or application wiring is needed.
type confirmationHTTPAuthority struct{}

func (confirmationHTTPAuthority) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	caller, _ := access.CallerFrom(ctx)
	return caller, nil
}

type confirmationHTTPSource struct{}

func (confirmationHTTPSource) ChecksIn(_ context.Context, _ database.Queryer, projects []ids.ID) (map[ids.ID][]conformance.DeclaredCheck, error) {
	out := make(map[ids.ID][]conformance.DeclaredCheck, len(projects))
	for _, id := range projects {
		out[id] = []conformance.DeclaredCheck{{
			Block: "hygiene", Version: 1,
			Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "clean", Evaluator: "llm", Rubric: "the tree is clean", Severity: "medium"},
		}}
	}
	return out, nil
}

func (confirmationHTTPSource) HistoricalChecksIn(context.Context, database.Queryer, []conformance.CheckVersion) ([]conformance.DeclaredCheck, error) {
	return nil, nil
}

func (confirmationHTTPSource) Head(context.Context, conformance.ProjectSource) (conformance.SourceHead, error) {
	return conformance.SourceHead{SHA: "fixture-head"}, nil
}

// confirmationHTTPContext is bls's verified caller; bls's account is
// recorded first, since conformance decides only for an enabled account.
func confirmationHTTPContext(t *testing.T, db *database.DB) context.Context {
	t.Helper()
	if _, err := identity.New(db, identity.Config{}).EnsureUser(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	return access.WithCaller(context.Background(), access.Caller{
		Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"},
		Verified:  true,
	})
}

func confirmationHTTPProject(t *testing.T, db *database.DB, slug string) (ids.ID, string) {
	t.Helper()
	projects := project.New(db, nil)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.SetDefaultBranch(context.Background(), id, "main"); err != nil {
		t.Fatal(err)
	}
	return id, slug
}

func confirmationHTTPVerdict(t *testing.T, service *conformance.Service, ctx context.Context, projectID ids.ID, slug string, state conformance.Outcome) conformance.Verdict {
	t.Helper()
	execution, err := service.BeginReview(ctx, conformance.ReviewRequest{Trigger: "manual", Projects: []ids.ID{projectID}})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := execution.Projects(ctx)
	if err != nil || len(planned) != 1 || planned[0].Slug != slug || len(planned[0].Checks) != 1 {
		t.Fatalf("plan: %+v %v", planned, err)
	}
	check := planned[0].Checks[0]
	pending, err := execution.PrepareVerdict(ctx, conformance.Assessment{
		ProjectID: projectID,
		Check:     conformance.CheckIdentity{ProjectID: projectID, Block: check.Block, Version: check.Version, Check: check.Check.Name},
		State:     state, Detail: "fixture verdict", EvaluatorVersion: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := pending.Commit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Progress(ctx, 1, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Finish(ctx, conformance.Completion{Projects: 1, ProjectsDone: 1}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	return verdict
}

func confirmationHTTPRequest(ctx context.Context, id ids.ID) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/conformance/verdicts/"+id.String()+"/confirm", strings.NewReader(`{"note":"checked"}`)).WithContext(ctx)
	r.SetPathValue("id", id.String())
	return r
}

func TestConfirmationHTTPDistinguishesCommandErrors(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projectID, slug := confirmationHTTPProject(t, db, "reviewed")
	projects := project.New(db, nil)
	service := conformance.New(db, confirmationHTTPAuthority{}, confirmationHTTPSource{}, nil)
	ctx := confirmationHTTPContext(t, db)
	flagged := confirmationHTTPVerdict(t, service, ctx, projectID, slug, conformance.DoesNotConform)
	handler := NewConformance(service, projects, nil)
	call := func(id ids.ID) int {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ConfirmVerdict(w, confirmationHTTPRequest(ctx, id))
		return w.Code
	}

	for _, body := range []string{`{`, `{"note":false}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("id", flagged.ID.String())
		w := httptest.NewRecorder()
		handler.ConfirmVerdict(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid confirmation: %d %s", w.Code, w.Body.String())
		}
	}
	if got := call(idstest.N(999999)); got != http.StatusNotFound {
		t.Fatalf("missing verdict status: %d", got)
	}
	t.Cleanup(dbtest.NextStatementFailure(db, "FROM conformance_adjudications"))
	if got := call(flagged.ID); got != http.StatusInternalServerError {
		t.Fatalf("read failure status: %d", got)
	}
	if got := call(flagged.ID); got != http.StatusCreated {
		t.Fatalf("success status: %d", got)
	}
	if got := call(flagged.ID); got != http.StatusConflict {
		t.Fatalf("already confirmed status: %d", got)
	}
	passingID, passingSlug := confirmationHTTPProject(t, db, "passing")
	passing := confirmationHTTPVerdict(t, service, ctx, passingID, passingSlug, conformance.Conforms)
	if got := call(passing.ID); got != http.StatusUnprocessableEntity {
		t.Fatalf("ineligible verdict status: %d", got)
	}
	// A review nobody ran is not found through the owner's classification,
	// under both the review page and the conformance lens.
	detail := httptest.NewRequest(http.MethodGet, "/api/v1/conformance/reviews/0000000f-423f-7000-8000-000000000000", nil).WithContext(ctx)
	detail.SetPathValue("id", "0000000f-423f-7000-8000-000000000000")
	w := httptest.NewRecorder()
	handler.ReviewDetail(w, detail)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing review detail: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.Conformance(w, httptest.NewRequest(http.MethodGet, "/api/v1/conformance?review=0000000f-423f-7000-8000-000000000000", nil).WithContext(ctx))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing review lens: %d %s", w.Code, w.Body.String())
	}
}
