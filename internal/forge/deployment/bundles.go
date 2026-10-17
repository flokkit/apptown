package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

// Release bundles (design/subsystems/deployment.md §2.8): the immutable thing
// that ships, recorded once by a landing. Every deployment of the project
// runs from a bundle; promotion never chooses images, it chooses a bundle.
//
// A bundle names its images one of two ways. A bundle of daemon image IDs
// (the landing built on its target) names the target whose daemon holds
// them. A digest bundle names every image as <registry>@sha256:<hex> in one
// registry, as that registry answered for it, built for one platform on a
// recorded worker (design/operations/workers.md, "Releases built on the
// worker"). Docker hosts pull a digest bundle's images by digest, wherever it goes.

type ReleaseBundle struct {
	ID               ids.ID            `json:"id"`
	ProjectID        ids.ID            `json:"projectId"`
	ContractRevision string            `json:"contractRevision"` // the tree sha
	ContractHash     string            `json:"contractHash"`
	Parts            map[string]string `json:"parts"` // part -> image id, or <registry>@sha256:<hex>
	Init             map[string]string `json:"init"`  // init step -> image id, or <registry>@sha256:<hex>
	BuiltFrom        BundleOrigin      `json:"builtFrom"`
	CreatedAt        string            `json:"createdAt"`
}

type BundleOrigin struct {
	ChangeID ids.ID `json:"changeId,omitzero"`
	RunID    ids.ID `json:"runId,omitzero"`
	Tree     string `json:"tree"`
	// TargetID is the target whose daemon the image IDs were read from:
	// they exist there and nowhere else. 0 is a bundle recorded before
	// releases named their target (its IDs were read from the forge's own
	// daemon), or a digest bundle. Kept in its own column.
	TargetID ids.ID `json:"targetId,omitzero"`
	// Registry is a digest bundle's repository, which holds every one of
	// its images; Platform the os/arch they were built for; Build where.
	// Empty for a bundle of daemon image IDs. Kept in their own columns.
	Registry string      `json:"registry,omitempty"`
	Platform string      `json:"platform,omitempty"`
	Build    BundleBuild `json:"build,omitzero"`
	// StepID and Attempt are the deploy step attempt that committed the
	// bundle; 0 for a bundle recorded outside one. Kept in their own
	// columns.
	StepID  ids.ID `json:"stepId,omitzero"`
	Attempt int64  `json:"attempt,omitempty"`
	// SelfServing says the bundle's static images carry the command that
	// serves them (docker.StaticSiteDockerfile), as every image built now
	// does. A bundle without it is the earlier form: its
	// static image holds only the files, and a target runs it with
	// docker.StaticServerCommand, as it always did. Kept in built_from_json.
	SelfServing bool `json:"selfServing,omitempty"`
	// By is the claimed deploy step attempt committing the bundle (Cut).
	// Never stored or read back.
	By AttemptClaim `json:"-"`
}

// ByDigest says whether b names its images by digest in its registry.
func (b *ReleaseBundle) ByDigest() bool { return b.BuiltFrom.Registry != "" }

// BundleBuild is where a digest bundle's images were built: the worker by
// its address and role, and the qualified profile it ran under, by its
// qualification record, or "" when unqualified.
type BundleBuild struct {
	Worker        string `json:"worker,omitempty"`
	Role          string `json:"role,omitempty"`
	Qualification string `json:"qualification,omitempty"`
}

// AttemptClaim is Work's claimed step attempt, able to fence a
// participant's write: InAttempt runs write in one transaction that first
// confirms, under lock, that the attempt of kind still owns its step, and
// hands it the attempt's number. A lost attempt writes nothing.
type AttemptClaim interface {
	ClaimedExecution
	InAttempt(kind string, write func(tx *database.Tx, attempt int64) error) error
}

type Bundles struct {
	db    *database.DB
	clock func() time.Time // the forge's clock, which stamps a bundle; nil is the real one
}

func NewBundles(db *database.DB) *Bundles { return &Bundles{db: db} }

func (s *Bundles) now() time.Time { return clock.Or(s.clock)() }

var digestImage = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

// Cut records a bundle: the image each part and init step runs, and the
// contract's hash. With from.By, the bundle is committed in one
// transaction with Work's check that the claimed deploy step attempt still
// owns its step, and names that attempt: a stale or superseded attempt
// commits nothing, and an attempt commits at most one bundle. A digest
// bundle is only ever committed so, and every image it names must be in its
// registry by digest. A bundle of daemon image IDs may be cut without a
// claim and names no attempt.
func (s *Bundles) Cut(ctx context.Context, projectID ids.ID, tree string, parts, init map[string]string, contractBytes []byte, from BundleOrigin) (ids.ID, error) {
	ctx = context.WithoutCancel(ctx) // the images are built: the bundle is recorded though ctx has ended
	if err := from.check(parts, init); err != nil {
		return ids.Nil, err
	}
	sum := sha256.Sum256(contractBytes)
	from.Tree = tree
	columns := from
	from.TargetID, from.Registry, from.Platform, from.Build, from.StepID, from.Attempt = ids.Nil, "", "", BundleBuild{}, ids.Nil, 0 // the columns hold them
	row := deploymentsql.InsertBundleParams{ID: ids.New(s.now().UTC()),
		ProjectID:        projectID,
		ContractRevision: tree,
		ContractHash:     hex.EncodeToString(sum[:]),
		PartsJson:        json.RawMessage(jsonText(parts)),
		InitJson:         json.RawMessage(jsonText(init)),
		CreatedAt:        s.now().UTC(),
		TargetID:         columns.TargetID,
		Registry:         columns.Registry,
		Platform:         columns.Platform,
		BuiltOnJson:      json.RawMessage(jsonText(columns.Build)),
	}
	if columns.By == nil {
		row.BuiltFromJson = json.RawMessage(jsonText(from))
		r, err := deploymentsql.New(s.db.SQL()).InsertBundle(ctx, row)
		return r.ID, err
	}
	claimedProject, runID, stepID, err := columns.By.ClaimedStep("deploy")
	if err != nil {
		return ids.Nil, err
	}
	if claimedProject != projectID || (!from.RunID.IsZero() && from.RunID != runID) {
		return ids.Nil, fmt.Errorf("a bundle for project %d run %d cannot be committed by run %d of project %d", projectID, from.RunID, runID, claimedProject)
	}
	from.RunID = runID
	row.BuiltFromJson = json.RawMessage(jsonText(from))
	var id ids.ID
	err = columns.By.InAttempt("deploy", func(tx *database.Tx, attempt int64) error {
		row.RunID, row.StepID, row.Attempt = runID, stepID, attempt
		r, err := deploymentsql.New(tx.SQL()).InsertBundle(ctx, row)
		id = r.ID
		return err
	})
	if err != nil {
		return ids.Nil, err
	}
	return id, nil
}

// check refuses a bundle whose form is not one of the two: a digest bundle
// names its registry, platform, build and committing attempt, and every
// image by digest in that registry; a bundle of daemon image IDs names none
// of these.
func (o BundleOrigin) check(parts, init map[string]string) error {
	if o.Registry == "" {
		if o.Platform != "" || o.Build != (BundleBuild{}) {
			return fmt.Errorf("a bundle names a platform or build only with its registry")
		}
		return nil
	}
	switch {
	case o.By == nil:
		return fmt.Errorf("a digest bundle is committed only by a claimed deploy step attempt")
	case o.Platform == "" || o.Build.Worker == "":
		return fmt.Errorf("a digest bundle names its platform and where it was built")
	case !o.TargetID.IsZero():
		return fmt.Errorf("a digest bundle's images are in its registry, not on a target")
	}
	for _, images := range []map[string]string{parts, init} {
		for name, ref := range images {
			if !strings.HasPrefix(ref, o.Registry+"@") || !digestImage.MatchString(ref) {
				return fmt.Errorf("a digest bundle's %s is %q, not an image by digest in %s", name, ref, o.Registry)
			}
		}
	}
	return nil
}

func bundleRecord(r deploymentsql.ReleaseBundle, err error) (*ReleaseBundle, error) {
	if err != nil {
		return nil, err
	}
	b := &ReleaseBundle{ID: r.ID, ProjectID: r.ProjectID, ContractRevision: r.ContractRevision, ContractHash: r.ContractHash, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano)}
	// Damaged evidence is an error, never read back as a bundle with no
	// parts or no origin. The build is read after the origin, which it
	// completes.
	for _, decode := range []struct {
		what string
		raw  json.RawMessage
		into any
	}{{"parts", r.PartsJson, &b.Parts}, {"init images", r.InitJson, &b.Init}, {"origin", r.BuiltFromJson, &b.BuiltFrom}, {"build", r.BuiltOnJson, &b.BuiltFrom.Build}} {
		if err := json.Unmarshal(decode.raw, decode.into); err != nil {
			return nil, fmt.Errorf("release bundle %d: reading its %s: %w", r.ID, decode.what, err)
		}
	}
	b.BuiltFrom.TargetID, b.BuiltFrom.Registry, b.BuiltFrom.Platform = r.TargetID, r.Registry, r.Platform
	b.BuiltFrom.StepID, b.BuiltFrom.Attempt = r.StepID, r.Attempt
	return b, nil
}

func (s *Bundles) ByID(ctx context.Context, id ids.ID) (*ReleaseBundle, error) {
	return bundleRecord(deploymentsql.New(s.db.SQL()).BundleByID(ctx, id))
}

// Find is the bundle with id, or nil when there is none.
func (s *Bundles) Find(ctx context.Context, id ids.ID) (*ReleaseBundle, error) {
	return lookupBundle(s.ByID(ctx, id))
}

// Latest is the newest bundle a project has cut, or nil.
func (s *Bundles) Latest(ctx context.Context, projectID ids.ID) (*ReleaseBundle, error) {
	return lookupBundle(bundleRecord(deploymentsql.New(s.db.SQL()).LatestBundle(ctx, projectID)))
}

// lookupBundle is a found bundle, or nil when there is none.
func lookupBundle(b *ReleaseBundle, err error) (*ReleaseBundle, error) {
	if found, err := database.Lookup(err); !found || err != nil {
		return nil, err
	}
	return b, nil
}

// DigestImages are the images a project's digest bundles name, each
// <registry>@sha256:<hex> as a Docker host pulled it, sorted and once each.
// A bundle of daemon image IDs names none: its images were built on the
// host, under the build-image names.
func (s *Bundles) DigestImages(ctx context.Context, projectID ids.ID) ([]string, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ProjectDigestBundles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var images []string
	for _, r := range rows {
		b, _ := bundleRecord(r, nil)
		for _, m := range []map[string]string{b.Parts, b.Init} {
			for _, ref := range m {
				images = append(images, ref)
			}
		}
	}
	slices.Sort(images)
	return slices.Compact(images), nil
}

// OfRun is the newest bundle a landing run cut for a project, or nil.
func (s *Bundles) OfRun(ctx context.Context, projectID, runID ids.ID) (*ReleaseBundle, error) {
	return lookupBundle(bundleRecord(deploymentsql.New(s.db.SQL()).BundleOfRun(ctx, deploymentsql.BundleOfRunParams{ProjectID: projectID, RunID: runID})))
}

// Images names the image each part and init step runs as the landing
// booted it, read from the daemon it booted on: a part with its own pushed
// image keeps that reference; a part built from the tree is the built
// image's ID.
func (r LandingResult) Images(ctx context.Context) (parts, init map[string]string) {
	parts, init = map[string]string{}, map[string]string{}
	c := r.Boot.Boot.Stack.Plan
	if c == nil {
		return parts, init
	}
	id := func(ref string) string {
		if ref == "" {
			return ""
		}
		if d := r.Docker.Text(ctx, "image", "inspect", "-f", "{{.Id}}", ref); d != "" {
			return d
		}
		return ref
	}
	for _, p := range c.Parts {
		if p.Image != "" {
			parts[p.Name] = id(p.Image)
		}
	}
	for _, s := range c.Init {
		if s.Image != "" {
			init[s.Name] = id(s.Image)
		}
	}
	return parts, init
}
