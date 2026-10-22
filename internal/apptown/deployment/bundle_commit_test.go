package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

var errAttemptLost = errors.New("the attempt no longer owns its step")

// claimedAttempt stands in for Work's claimed deploy step attempt: it
// fences a write in a transaction, as Work does, while it is current, and
// refuses once lost. failAfter makes the fence's own transaction fail after
// the write, as Work's commit can.
type claimedAttempt struct {
	db                 *database.DB
	project, run, step ids.ID
	at                 int64
	lost, failAfter    bool
	// lostAfter, when positive, loses the attempt after that many fenced
	// writes: a later attempt has superseded it.
	lostAfter, fenced int
}

func (c *claimedAttempt) ClaimedStep(kind string) (ids.ID, ids.ID, ids.ID, error) {
	if kind != "deploy" {
		return ids.Nil, ids.Nil, ids.Nil, errAttemptLost
	}
	return c.project, c.run, c.step, nil
}

func (c *claimedAttempt) InAttempt(kind string, write func(*database.Tx, int64) error) error {
	if c.lost || kind != "deploy" || (c.lostAfter > 0 && c.fenced >= c.lostAfter) {
		return errAttemptLost
	}
	c.fenced++
	return c.db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := write(tx, c.at); err != nil {
			return err
		}
		if c.failAfter {
			return errAttemptLost
		}
		return nil
	})
}

const (
	releaseRepo = "registry.example.com/team/game"
	digestA     = "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB     = "@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func digestOrigin(by AttemptClaim) BundleOrigin {
	return BundleOrigin{Registry: releaseRepo, Platform: "linux/amd64", Build: BundleBuild{Worker: "http://worker:7070", Role: "agent", Qualification: "design/qualification/worker-vm.md"}, By: by}
}

// A bundle committed by a claimed deploy step attempt is committed only
// while that attempt owns its step, in the fence's own transaction, and
// once per attempt; it names the attempt that committed it.
func TestABundleIsCommittedOnlyByTheAttemptThatOwnsTheStep(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := createTestProject(t, db, "game", "Game")
	bundles := NewBundles(db)
	parts := map[string]string{"web": releaseRepo + digestA}
	cut := func(c *claimedAttempt) (ids.ID, error) {
		return bundles.Cut(t.Context(), project, "tree", parts, nil, []byte("app: {}"), digestOrigin(c))
	}
	if _, err := cut(&claimedAttempt{db: db, project: project, run: idstest.N(7), step: idstest.N(70), at: 1, lost: true}); !errors.Is(err, errAttemptLost) {
		t.Fatalf("a stale attempt: %v", err)
	}
	if _, err := cut(&claimedAttempt{db: db, project: project, run: idstest.N(7), step: idstest.N(70), at: 1, failAfter: true}); !errors.Is(err, errAttemptLost) {
		t.Fatalf("a fence whose transaction failed: %v", err)
	}
	if b, err := bundles.Latest(context.Background(), project); err != nil || b != nil {
		t.Fatalf("a stale attempt or failed fence left a bundle: %+v %v", b, err)
	}
	current := &claimedAttempt{db: db, project: project, run: idstest.N(7), step: idstest.N(70), at: 2}
	id, err := cut(current)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundles.ByID(context.Background(), id)
	if err != nil || b.BuiltFrom.RunID != idstest.N(7) || b.BuiltFrom.StepID != idstest.N(70) || b.BuiltFrom.Attempt != 2 {
		t.Fatalf("the committed bundle names %+v %v", b, err)
	}
	if _, err := cut(current); err == nil || !strings.Contains(err.Error(), "release_bundles_attempt") {
		t.Fatalf("a second bundle by the same attempt: %v", err)
	}
	if latest, err := bundles.Latest(context.Background(), project); err != nil || latest.ID != id {
		t.Fatalf("the attempt's one bundle: %+v %v", latest, err)
	}
	other := createTestProject(t, db, "other", "Other")
	if _, err := cut(&claimedAttempt{db: db, project: other, run: idstest.N(8), step: idstest.N(80), at: 1}); err == nil {
		t.Fatal("an attempt of another project committed this project's bundle")
	}
}

// A digest bundle records its registry, platform and build, and names every
// image by digest in that registry. Anything else is refused before it is
// written, and a bundle of daemon image IDs names none of these.
func TestADigestBundleNamesItsRegistryPlatformAndBuild(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := createTestProject(t, db, "game", "Game")
	bundles := NewBundles(db)
	claim := &claimedAttempt{db: db, project: project, run: idstest.N(7), step: idstest.N(70), at: 1}
	parts, init := map[string]string{"web": releaseRepo + digestA, "cache": releaseRepo + digestB}, map[string]string{"migrate": releaseRepo + digestA}
	// Each is refused with its reason, before the database's own form
	// constraint (which also refuses a digest bundle with no attempt or with
	// a target) is reached.
	for name, c := range map[string]struct {
		parts, init map[string]string
		from        BundleOrigin
		says        string
	}{
		"no claim":                 {parts, init, digestOrigin(nil), "only by a claimed deploy step attempt"},
		"no platform":              {parts, init, func() BundleOrigin { o := digestOrigin(claim); o.Platform = ""; return o }(), "names its platform"},
		"no build":                 {parts, init, func() BundleOrigin { o := digestOrigin(claim); o.Build = BundleBuild{}; return o }(), "where it was built"},
		"a target":                 {parts, init, func() BundleOrigin { o := digestOrigin(claim); o.TargetID = idstest.N(3); return o }(), "not on a target"},
		"an image by tag":          {map[string]string{"web": releaseRepo + ":latest"}, nil, digestOrigin(claim), "not an image by digest"},
		"an image by a bad digest": {map[string]string{"web": releaseRepo + "@sha256:abc"}, nil, digestOrigin(claim), "not an image by digest"},
		"an image elsewhere":       {map[string]string{"web": "other.example.com/team/game" + digestA}, nil, digestOrigin(claim), "not an image by digest"},
		"an init image by ID":      {parts, map[string]string{"migrate": "sha256:" + strings.Repeat("a", 64)}, digestOrigin(claim), "not an image by digest"},
		"daemon IDs, a platform":   {map[string]string{"web": "sha256:abc"}, nil, BundleOrigin{Platform: "linux/amd64"}, "only with its registry"},
		"daemon IDs, a build":      {map[string]string{"web": "sha256:abc"}, nil, BundleOrigin{Build: BundleBuild{Worker: "w"}}, "only with its registry"},
	} {
		if _, err := bundles.Cut(t.Context(), project, "tree", c.parts, c.init, nil, c.from); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want refused as %q", name, err, c.says)
		}
	}
	if b, err := bundles.Latest(context.Background(), project); err != nil || b != nil {
		t.Fatalf("a refused bundle was written: %+v %v", b, err)
	}
	id, err := bundles.Cut(t.Context(), project, "tree", parts, init, []byte("app: {}"), digestOrigin(claim))
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundles.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := digestOrigin(nil)
	if got := b.BuiltFrom; got.Registry != want.Registry || got.Platform != want.Platform || got.Build != want.Build || !got.TargetID.IsZero() ||
		b.Parts["web"] != releaseRepo+digestA || b.Parts["cache"] != releaseRepo+digestB || b.Init["migrate"] != releaseRepo+digestA {
		t.Fatalf("the digest bundle reads back as %+v", b)
	}
	legacy, err := bundles.Cut(t.Context(), project, "tree", map[string]string{"web": "sha256:abc"}, nil, nil, BundleOrigin{TargetID: idstest.N(4)})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := bundles.ByID(context.Background(), legacy); err != nil || b.BuiltFrom.Registry != "" || b.BuiltFrom.Platform != "" || b.BuiltFrom.Build != (BundleBuild{}) || b.BuiltFrom.Attempt != 0 || b.BuiltFrom.TargetID != idstest.N(4) {
		t.Fatalf("a bundle of daemon image IDs reads back as %+v %v", b, err)
	}
}

// A generation pins the bundle it ran: after a later attempt of the same
// run commits another bundle, the generation still names its own, though
// the run's newest bundle has moved on.
func TestAGenerationFollowsItsPinnedBundleAfterALaterAttemptCommitsAnother(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := createTestProject(t, db, "game", "Game")
	s := &Service{db: db, Bundles: NewBundles(db)}
	cut := func(at int64, digest string) ids.ID {
		t.Helper()
		id, err := s.Bundles.Cut(t.Context(), project, "tree", map[string]string{"web": releaseRepo + digest}, nil, nil,
			digestOrigin(&claimedAttempt{db: db, project: project, run: idstest.N(7), step: idstest.N(70), at: at}))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := cut(1, digestA)
	in := Inputs{ReleaseRun: idstest.N(7), BundleID: first}
	second := cut(2, digestB)
	got, err := s.generationBundle(context.Background(), project, in)
	if err != nil || got == nil || got.ID != first {
		t.Fatalf("the generation ran %+v %v, want bundle %d", got, err, first)
	}
	if newest, err := s.Bundles.OfRun(context.Background(), project, idstest.N(7)); err != nil || newest == nil || newest.ID != second {
		t.Fatalf("the run's newest bundle: %+v %v, want %d", newest, err, second)
	}
}

// A bundle's recorded evidence that cannot be read is an error, never read
// back as a bundle with no parts, no init images or no origin.
func TestABundleWithUnreadableEvidenceIsAnError(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := createTestProject(t, db, "bundle-evidence", "Bundle Evidence")
	s := New(context.Background(), db, Dependencies{})
	for _, column := range []string{"parts_json", "init_json", "built_from_json", "built_on_json"} {
		id, err := s.Bundles.Cut(t.Context(), project, "tree", map[string]string{"web": "sha256:web"}, nil, []byte("app: {}\n"), BundleOrigin{})
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: no operation records evidence of the wrong shape; this proves a damaged record is not read as empty.
		if _, err := db.Exec(`UPDATE release_bundles SET `+column+`='["damaged"]'::jsonb WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if b, err := s.Bundles.ByID(context.Background(), id); err == nil {
			t.Fatalf("%s damaged, the bundle read back as %+v", column, b)
		}
	}
}
