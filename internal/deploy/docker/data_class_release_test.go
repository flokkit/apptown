package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// postgresNeed is a catalog with one need, Postgres, keeping its data at
// the usual path.
type postgresNeed struct{}

func (postgresNeed) Fulfil(ctx context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	if !slices.Contains(needs, "postgres") {
		return nil, nil, nil
	}
	return []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_DB": "app"}}}, map[string]string{"DATABASE_URL": "postgres://postgres/app"}, nil
}
func (postgresNeed) Data(context.Context, string) (string, error) {
	return "/var/lib/postgresql/data", nil
}

// A release through the driver boots with the deployment's data
// class. A production or non-production deployment, or one of no known
// class, whose Postgres was made from another image than declared now,
// keeps its container and its recorded data volume: the release is
// refused. Only a declared synthetic deployment is reset.
func TestAReleaseThroughTheDriverResetsOnlySyntheticData(t *testing.T) {
	for _, class := range []string{"production", "non-production", "", "synthetic"} {
		t.Run(fmt.Sprintf("class %q", class), func(t *testing.T) {
			t.Parallel()
			cname := Current.Service(ByKey(idstest.N(7)).Stack(), "postgres")
			vol := Current.NeedVolume(ByKey(idstest.N(7)).Stack(), "postgres")
			h := newOwnedHost("container "+cname, "volume "+vol, "network "+ByKey(idstest.N(7)).Network())
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case strings.Contains(line, "{{.Config.Image}}"):
					return []byte("postgres:15\n"), nil // made from another image
				case strings.HasPrefix(line, "image inspect --format {{.Id}}"):
					return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
				}
				return h.reply(req)
			}}
			d := &Driver{
				Scope: func(context.Context) (Scope, error) { return Scope{Client: client}, nil },
				Boot:  NewHost(Dependencies{Needs: postgresNeed{}, Secrets: reviewNoSecrets{}}),
				URL:   func(_, _ string, port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) },
				Owner: recordNothing{},
				Owned: func(context.Context) ([]deploy.Owned, error) {
					return []deploy.Owned{{Kind: "volume", ID: vol, Data: true, Need: "postgres"}}, nil
				},
				DataClass: class,
			}
			c, err := appcontract.Parse([]byte("app:\n  web:\n    image: app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n"))
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"version":1,"revision":"abc"}`)
			art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
				Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "shop", Contract: c}}
			_, err = d.Reconcile(context.Background(), deploy.Placement{ID: idstest.N(7), Label: "7", Name: "staging"}, 2, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
			if class != "synthetic" {
				if err == nil || !strings.Contains(err.Error(), "will not replace") {
					t.Fatalf("a %q deployment's database was replaced: %v", class, err)
				}
				if len(h.removed) != 0 {
					t.Fatalf("a refused release removed %v", h.removed)
				}
				return
			}
			if !slices.Contains(h.removed, "container "+cname) || !slices.Contains(h.removed, "volume "+vol) {
				t.Fatalf("the synthetic database was not reset: %v (%v)", h.removed, err)
			}
		})
	}
}

type recordNothing struct{}

func (recordNothing) Created(context.Context, deploy.Owned) error { return nil }
