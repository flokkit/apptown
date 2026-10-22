package apptown

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids"
)

// Identity and grants are separate: these contexts supply identity only.
func testConformanceContext(who string) context.Context {
	return verifiedContext(context.Background(), access.Principal{Kind: "user", Name: who, Via: "open"})
}

// savedReader is the fixture reader "t" with the account reads are decided
// for.
func savedReader(a *App) (context.Context, error) {
	_, err := a.Access.EnsureUser(context.Background(), "t")
	return testConformanceContext("t"), err
}
func savedReview(a *App, id ids.ID) (*conformance.Review, error) {
	ctx, err := savedReader(a)
	if err != nil {
		return nil, err
	}
	d, err := a.conformance.Review(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.Review, nil
}
func savedVerdicts(a *App, id ids.ID) ([]conformance.Verdict, error) {
	ctx, err := savedReader(a)
	if err != nil {
		return nil, err
	}
	d, err := a.conformance.Review(ctx, id)
	if err != nil {
		return nil, err
	}
	return d.Verdicts, nil
}
func savedLatestVerdict(a *App, id ids.ID, block string, version int, check string) (conformance.Verdict, error) {
	ctx, err := savedReader(a)
	if err != nil {
		return conformance.Verdict{}, err
	}
	v, err := a.conformance.View(ctx, ids.Nil)
	if err != nil {
		return conformance.Verdict{}, err
	}
	for _, project := range v.ByProject {
		for _, o := range project {
			if o.ProjectID == id && o.Block == block && o.Version == version && o.Check == check && o.Record != nil {
				return *o.Record, nil
			}
		}
	}
	return conformance.Verdict{}, sql.ErrNoRows
}

func identifiedTestRequest(method, target string, body io.Reader) *http.Request {
	return testRequestAs(httptest.NewRequest(method, target, body).WithContext(testConformanceContext("t")), "t")
}

// Conformance execution tests use an explicitly authorized operator. Tests of
// authorization itself start with testApp and supply their own scoped grants.
func conformanceTestApp(t *testing.T, configs ...Config) *App {
	t.Helper()
	a := testApp(t, configs...)
	testAdministrator(t, a, "t")
	return a
}
