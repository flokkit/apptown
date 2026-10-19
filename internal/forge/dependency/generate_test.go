package dependency

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/stock"
)

// A kind may generate only its own credentials, and only when its local
// service sets them again at every start: one that kept its first value in
// its data would refuse every later deploy of real data.
func TestAKindGeneratesOnlyItsCredentialsAndSetsThemAtEveryStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, yaml, refused string }{
		{"not a credential", "capability: x\ncredentials: {password: p}\ngenerate: [token]\nlocal:\n  image: x\n  configAtStart: true\n", `generates "token"`},
		{"kept in its data", "capability: x\ncredentials: {password: p}\ngenerate: [password]\nlocal:\n  image: x\n", "configAtStart"},
		{"set at every start", "capability: x\ncredentials: {password: p}\ngenerate: [password]\nlocal:\n  image: x\n  configAtStart: true\n", ""},
	} {
		_, err := Parse(tc.yaml)
		if tc.refused == "" && err != nil || tc.refused != "" && (!errors.Is(err, ErrSpec) || !strings.Contains(err.Error(), tc.refused)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// An owner's generated values reach the service's environment, what the
// app is given and a copy's connection, in place of the catalogue's; a
// credential it does not generate, and every value without an owner (the
// forge's own throwaway servers), stay the catalogue's. The service names
// the generated values so a boot can redact them.
func TestAnOwnersGeneratedCredentialsReplaceTheCatalogues(t *testing.T) {
	t.Parallel()
	k := stockKind(t, "s3")
	generated, err := k.Generated(t.Context(), func(_ context.Context, capability, name string) (string, error) {
		return "made-" + capability + "-" + name, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := k.ResolveLocal(nil, "", generated)
	if s.Env["APP_ACCESS_KEY_ID"] != "made-s3-user" || s.Env["ROOT_SECRET_ACCESS_KEY"] != "made-s3-root_secret" || s.Inject["AWS_SECRET_ACCESS_KEY"] != "made-s3-password" {
		t.Fatalf("the generated values: env %v inject %v", s.Env, s.Inject)
	}
	if s.Env["BUCKET"] != "app" || s.Inject["S3_BUCKET"] != "app" {
		t.Fatalf("the bucket's name is the catalogue's: %v", s.Env)
	}
	if want := []string{"made-s3-password", "made-s3-root_key", "made-s3-root_secret", "made-s3-user"}; !slices.Equal(s.Spec().Credentials, want) {
		t.Fatalf("the service's credentials: %v", s.Spec().Credentials)
	}
	if conn, _ := k.LocalConnection(nil, generated); conn.User != "made-s3-user" || conn.Password != "made-s3-password" {
		t.Fatalf("the copy's connection: %+v", conn)
	}
	plain := k.ResolveLocal(nil, "", nil)
	if plain.Env["APP_ACCESS_KEY_ID"] != "local" || len(plain.Spec().Credentials) != 0 {
		t.Fatalf("without an owner, the catalogue's: %v %v", plain.Env, plain.Spec().Credentials)
	}
	if _, err := k.Generated(t.Context(), func(context.Context, string, string) (string, error) { return "", nil }); err == nil {
		t.Fatal("an empty generated value was given")
	}
}

// Every credential of every stock kind is generated for each deployment and
// preview, except the ones named here with why: a new credential added to
// a stock kind is a constant every deployment shares until it is listed
// either way.
func TestEveryStockCredentialIsGeneratedOrSaidWhyNot(t *testing.T) {
	t.Parallel()
	shared := map[string]string{
		"s3.database": "the bucket's name: not a secret, the same everywhere, and AppTown's so an app cannot choose it",
	}
	files, err := fs.Glob(stock.FS, "dependencies/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		k := stockKind(t, strings.TrimSuffix(path.Base(f), ".yaml"))
		for name := range k.Credentials {
			if _, ok := shared[k.Capability+"."+name]; !ok && !slices.Contains(k.Generate, name) {
				t.Errorf("the stock %s's %s is a constant every deployment shares", k.Capability, name)
			}
		}
	}
}
