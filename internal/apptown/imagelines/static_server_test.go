package imagelines

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/imageline"
)

// staticReadings answers the server line as one fixed reading, or refuses,
// and counts what it was asked.
type staticReadings struct {
	asked   []string
	refused error
}

func (r *staticReadings) Answer(_ context.Context, ref string, scope imageline.Scope) (Reading, error) {
	r.asked = append(r.asked, ref+" "+string(scope))
	if r.refused != nil {
		return Reading{}, r.refused
	}
	return Reading{Answer: imageline.Answer{Tag: "index.docker.io/library/caddy:2.99.1-alpine", Digest: "sha256:" + strings.Repeat("b", 64), Registry: "registry.test"}, ReadAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}, nil
}

func (r *staticReadings) Digest(context.Context, string) (string, error) {
	return "", errors.New("not asked")
}

func parseContract(t *testing.T, yaml string) *appcontract.Contract {
	t.Helper()
	c, err := appcontract.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A static contract with no staticServer: is served with the newest build
// read on the server line, as tag@digest, with the reading's evidence; the
// caller's contract is left as it was parsed.
//
// Kills: a contract without a pin left unserved (the rc.16 refusal), a line
// other than caddy:2-alpine, a tag without its digest, a parsed contract
// changed in place.
func TestAnUnpinnedStaticContractIsServedWithTheNewestBuildOnTheLine(t *testing.T) {
	t.Parallel()
	r := &staticReadings{}
	c := parseContract(t, "app:\n  web: {static: ., public: true}\n")
	served, evidence, err := StaticServer(t.Context(), r, c)
	if err != nil {
		t.Fatal(err)
	}
	want := "index.docker.io/library/caddy:2.99.1-alpine@sha256:" + strings.Repeat("b", 64)
	if served.StaticServer != want || !strings.Contains(evidence, "registry reading from 2026-11-20") {
		t.Fatalf("served %q, evidence %q", served.StaticServer, evidence)
	}
	if len(r.asked) != 1 || r.asked[0] != "caddy:2-alpine line" {
		t.Fatalf("asked %v", r.asked)
	}
	if c.StaticServer != "" {
		t.Fatalf("the parsed contract was changed: %q", c.StaticServer)
	}
	if err := appcontract.ValidateStaticServer(served.StaticServer); err != nil {
		t.Fatal(err)
	}
}

// A pin is served as written and a contract without static parts is left
// alone, neither asking the registry; a reading that fails is an error that
// names the line and why, never a default image.
func TestAPinIsKeptAndAFailedReadingIsRefused(t *testing.T) {
	t.Parallel()
	r := &staticReadings{}
	pin := "caddy:2.11.9-alpine@sha256:" + strings.Repeat("a", 64)
	for _, yaml := range []string{"staticServer: " + pin + "\napp:\n  web: {static: ., public: true}\n", "app:\n  web: {image: app:1, public: true}\n"} {
		c := parseContract(t, yaml)
		served, evidence, err := StaticServer(t.Context(), r, c)
		if err != nil || served != c || evidence != "" {
			t.Fatalf("%s: %+v %q %v", yaml, served, evidence, err)
		}
	}
	if len(r.asked) != 0 {
		t.Fatalf("asked %v", r.asked)
	}
	r.refused = errors.New("registry offline")
	_, _, err := StaticServer(t.Context(), r, parseContract(t, "app:\n  web: {static: ., public: true}\n"))
	if err == nil || !strings.Contains(err.Error(), "caddy:2-alpine") || !strings.Contains(err.Error(), "registry offline") {
		t.Fatal(err)
	}
}

func (r *staticReadings) Cached(ctx context.Context, ref string, scope imageline.Scope) (Reading, bool, error) {
	a, e := r.Answer(ctx, ref, scope)
	return a, e == nil, e
}
