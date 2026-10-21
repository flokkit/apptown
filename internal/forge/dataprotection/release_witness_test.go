package dataprotection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
)

// afterDaemon answers what a release witness asks after a release: where
// the need's data is mounted, when that volume was made, and its measure.
type afterDaemon struct {
	mounts, created, measure string
	measureErr               error
}

func (d afterDaemon) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	args := strings.Join(req.Args, " ")
	switch {
	case strings.HasPrefix(args, "inspect") && strings.Contains(args, ".Mounts"):
		return []byte(d.mounts), nil
	case strings.HasPrefix(args, "volume inspect"):
		return []byte(d.created + "\n"), nil
	case strings.HasPrefix(args, "exec"):
		return []byte(d.measure), d.measureErr
	}
	return nil, nil
}

const (
	pgData   = "/var/lib/postgresql/data"
	pgVolume = "forge-need-env-shop-main-postgres"
	madeThen = "2026-09-08T03:53:13Z"
)

func witnessed(d afterDaemon, synthetic bool, before map[string]int64) *ReleaseWitness {
	return &ReleaseWitness{synthetic: synthetic, dk: docker.Scope{Client: d}, needs: []needFacts{{
		need: "postgres", container: "forge-svc-env-shop-main-postgres", path: pgData, measure: "measure",
		mount: docker.DataMount{Volume: pgVolume, Found: true}, created: madeThen, measures: before,
	}}}
}

// A release's data continuity check: the data stays on the storage it was
// on, and no measure of it falls by more than half from a size worth
// comparing; a deliberate reset of synthetic data is noted, not failed; a
// measure that answered before and cannot be read after is a failure, not
// a pass.
func TestAReleaseWitnessesItsNeedsData(t *testing.T) {
	t.Parallel()
	same := "volume\t" + pgVolume + "\t/x\t" + pgData + "\n"
	before := map[string]int64{"tables": 5, "rows": 120, "bytes": 8 << 20}
	for _, c := range []struct {
		name      string
		d         afterDaemon
		synthetic bool
		before    map[string]int64
		problem   string // "" for none
		note      string
	}{
		{name: "the same data", d: afterDaemon{mounts: same, created: madeThen, measure: "tables 5\nrows 118\nbytes 8388608\n"}, before: before},
		{name: "rows fell", d: afterDaemon{mounts: same, created: madeThen, measure: "tables 5\nrows 3\nbytes 8388608\n"}, before: before, problem: "rows fell from 120 to 3"},
		{name: "tables went", d: afterDaemon{mounts: same, created: madeThen, measure: "tables 0\nrows 0\nbytes 8388608\n"}, before: before, problem: "had 5 tables and now has none"},
		{name: "a small app's churn", d: afterDaemon{mounts: same, created: madeThen, measure: "tables 2\nrows 1\nbytes 900000\n"}, before: map[string]int64{"tables": 2, "rows": 15, "bytes": 900000}},
		{name: "unreadable after", d: afterDaemon{mounts: same, created: madeThen, measure: "psql: connection refused", measureErr: errors.New("exit status 2")}, before: before, problem: "could not be read after the release"},
		{name: "on other storage", d: afterDaemon{mounts: "volume\tf0e1d2\t/x\t" + pgData + "\n", created: "2026-10-21T00:59:14Z", measure: "tables 5\nrows 120\nbytes 8388608\n"}, before: before, problem: "was on volume " + pgVolume},
		{name: "the same name, made again", d: afterDaemon{mounts: same, created: "2026-10-21T00:59:14Z", measure: "tables 5\nrows 120\nbytes 8388608\n"}, before: before, problem: "is now on volume " + pgVolume + " (made 2026-10-21"},
		{name: "a synthetic reset", d: afterDaemon{mounts: same, created: "2026-10-21T00:59:14Z", measure: "tables 0\nrows 0\nbytes 1000\n"}, synthetic: true, before: before, note: "synthetic data was reset"},
		{name: "not measured before", d: afterDaemon{mounts: same, created: madeThen, measure: ""}, before: nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			problems, notes := witnessed(c.d, c.synthetic, c.before).After(context.Background(), []string{"postgres"})
			got := strings.Join(problems, "; ")
			if c.problem == "" && got != "" || c.problem != "" && !strings.Contains(got, c.problem) {
				t.Fatalf("problems %q, want %q", got, c.problem)
			}
			if c.note != "" && !strings.Contains(strings.Join(notes, "; "), c.note) {
				t.Fatalf("notes %q, want %q", notes, c.note)
			}
		})
	}
}

// A need the new release no longer declares is not compared.
func TestANeedNoLongerDeclaredIsNotCompared(t *testing.T) {
	t.Parallel()
	w := witnessed(afterDaemon{mounts: "", measure: ""}, false, map[string]int64{"rows": 500})
	problems, notes := w.After(context.Background(), nil)
	if len(problems) != 0 || !strings.Contains(strings.Join(notes, "; "), "no longer declared") {
		t.Fatalf("problems %q, notes %q", problems, notes)
	}
}
