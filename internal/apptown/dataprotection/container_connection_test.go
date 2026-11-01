package dataprotection

import (
	"io/fs"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/stock"
)

// A copy reaches a running need with what its container was started with:
// the deployment's own credentials, or the constants a container made
// before credentials were generated still serves, never the catalogue's
// over them. A restore into another deployment reaches the destination
// with the destination's own, by the same reading.
func TestACopyReachesANeedWithTheCredentialsItsContainerWasStartedWith(t *testing.T) {
	t.Parallel()
	kind := func(name string) *dependency.Kind {
		raw, err := fs.ReadFile(stock.FS, "dependencies/"+name+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		k, err := dependency.Parse(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	for _, tc := range []struct {
		kind, env            string
		user, password, base string
	}{
		{"postgres", `"POSTGRES_DB=ledger","POSTGRES_USER=keeper","POSTGRES_PASSWORD=its-own"`, "keeper", "its-own", "ledger"},
		{"mariadb", `"MARIADB_DATABASE=app","MARIADB_USER=app","MARIADB_PASSWORD=its-own"`, "app", "its-own", "app"},
		{"s3", `"APP_ACCESS_KEY_ID=its-key","APP_SECRET_ACCESS_KEY=its-own","BUCKET=app","ROOT_ACCESS_KEY_ID=root"`, "its-key", "its-own", "app"},
		// A container from before: the constants it was started with.
		{"postgres", `"POSTGRES_DB=app","POSTGRES_USER=app","POSTGRES_PASSWORD=app"`, "app", "app", "app"},
	} {
		client := newDockerFake()
		client.SetReply(func(args []string) ([]byte, error, bool) {
			if len(args) == 4 && args[0] == "inspect" && args[2] == "{{json .Config}}" {
				return []byte(`{"Image":"x","Env":[` + tc.env + `]}`), nil, true
			}
			return nil, nil, false
		})
		conn, _, err := containerConnection(t.Context(), docker.Scope{Client: client}, kind(tc.kind), "c")
		if err != nil {
			t.Fatal(err)
		}
		if conn.User != tc.user || conn.Password != tc.password || conn.Database != tc.base || conn.Host != "127.0.0.1" {
			t.Errorf("%s (%s): %+v", tc.kind, tc.env, conn)
		}
	}
}
