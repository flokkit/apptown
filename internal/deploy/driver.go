package deploy

import (
	"context"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// The words a target driver and its caller share: what to run, where,
// and what was seen.

// Artifact is a renderer's output: canonical bytes whose hash can sit in
// a receipt, carrying secret references and never values.
type Artifact struct {
	Kind string // compose | do-app-spec | docker-source | ...
	Body []byte
	Hash string
	// Source is the selected software the artifact was rendered from, for
	// drivers that build from source on the host (the built-in Docker
	// host). Never serialized: the receipt carries Body and Hash, and a
	// staged workspace belongs to one execution.
	Source *Source
}

// Source is the selected software a deploy runs from: a bundle cut
// earlier, or a committed tree the caller staged. The caller owns the
// workspace's lifetime.
type Source struct {
	// Services is a replay of supplied sidecars after applying the generation
	// record; nil uses the original artifact inputs. It never changes Body/Hash.
	Services  []ServiceSpec
	Workspace string // a staged, self-contained checkout of Revision
	Revision  string // the committed tree the source is
	Slug      string // the application's slug, for naming on hosts
	Contract  *appcontract.Contract
	Images    map[string]string // part -> image id, or image by digest, when the parts were built earlier
	Init      map[string]string // init step -> image id, likewise
	// SelfServing: the static images in Images carry their own command
	// (the bundle records it); false is a bundle of the earlier form.
	SelfServing bool
	// Published: Images and Init are images by digest in a registry, built
	// for Platform; false is image IDs that live only on the host that
	// built them.
	Published bool
	Platform  string
}

// Renderer is a driver that renders the target's artifact from the
// selected source (App Platform: the app spec with pushed images).
type Renderer interface {
	Render(ctx context.Context, p Placement, src Source, bindings NeedBindings, secrets ConsumerSecrets, log Logf) (Artifact, error)
}

// Preflighter is a driver that can refuse a release before any effect,
// from a read of the target's own limits (App Platform: whether its
// container registry has room for the project's repository) or from what
// the release would send it (App Platform: a value it would rewrite). It
// changes nothing; a refusal fails the release before its needs are
// fulfilled or its artifact rendered. A limit it cannot read is not a
// refusal: the effect that needs the room is still the authority.
type Preflighter interface {
	Preflight(ctx context.Context, p Placement, src Source, secrets ConsumerSecrets, log Logf) error
}

// SizePricer is a target that bills per instance size: its listed monthly
// price of each size, by slug, the start of each size words a fitter
// answers (ResourceFitter). An estimate is made from it, and called one.
type SizePricer interface {
	SizePrices(ctx context.Context) (map[string]float64, error)
}

// ResourceFitter is a driver whose target rounds a part's declared
// resources to sizes of its own (App Platform's instance sizes). Asked
// before any need is fulfilled or artifact rendered, it answers what each
// part will run on, in words ("apps-s-1vcpu-1gb (1G, 1 CPU)"), or an error
// that stops the release before any effect: a declaration no size covers
// is terminal; sizes that cannot be read are not. A driver that holds a
// declaration exactly answers no sizes, and may still refuse one larger
// than its host can give (a Docker host).
type ResourceFitter interface {
	FitResources(ctx context.Context, c *appcontract.Contract) (map[string]string, error)
}

// NeedFulfiller is a driver that provisions the contract's needs on its
// target (App Platform: a managed database) and reports the binding and
// the provider's handle.
type NeedFulfiller interface {
	FulfilNeed(ctx context.Context, p Placement, need string, params map[string]string, log Logf) (NeedBinding, map[string]any, error)
}

// Controls says what the target owns: when it controls liveness the
// health controller steps back; when it controls rollout AppTown waits
// on it rather than probing containers itself.
type Controls struct {
	Liveness bool
	Rollout  bool
}

// NeedBindings is what reconcile hands the renderer: per need, the
// connection the app should be given, as the target expresses it.
type NeedBindings map[string]NeedBinding

type NeedBinding struct {
	Env map[string]string // variables the app reads (DATABASE_URL, PG*)
	// Secret are variables the app reads whose values are credentials (an
	// object store's key secret): delivered as the target's secrets, never
	// in a receipt.
	Secret map[string]string
	Native map[string]any // target-native reference (an App Platform database component)
}

// Logf receives the human-readable lines of a release's record.
type Logf = func(string, ...any)

// Driver is the desired-state contract every target kind implements
// (deployment.md §2.4). The drivers live in the packages under this one
// and implement it directly; AppTown registers them by kind.
type Driver interface {
	// Reconcile brings the deployment to the artifact as the given
	// generation. The same idempotency key is the same attempt; checkpoint
	// records the provider's identifiers on the attempt as soon as they
	// exist, so recovery can use them.
	Reconcile(ctx context.Context, p Placement, generation int64, art Artifact, key string, log Logf, checkpoint Checkpoint) (Observation, error)
	// Resume picks up an attempt AppTown was interrupted in: given what
	// was noted, find out what the target did and finish the attempt,
	// active, failed, or honestly unknown.
	Resume(ctx context.Context, p Placement, generation int64, noted map[string]any, log Logf) (Observation, error)
	Observe(ctx context.Context, p Placement) (Observation, error)
	// Teardown removes exactly the recorded generation's deployment and
	// deletes what AppTown recorded creating for it (td.Owned): data
	// only when td.BackedUp says a final backup covers it. It never
	// deletes what is not on the list. An error means the serving thing
	// (container, app, stack, bucket) may still stand; anything else it
	// could not delete, and data it keeps, it reports as Left.
	Teardown(ctx context.Context, p Placement, generation int64, td TakeDown) (TakenDown, error)
	Controls() Controls
}

// Left is one thing a teardown left at the target: what it is, in words
// the ledger shows ("image repository"), and the provider's identity of it.
// Handle carries "name" and, when known, "size"; "kind" names its inventory
// kind when it is not a database. Data marks what holds data; Why says why
// it was left (kept: no backup here; or the delete's error).
type Left struct {
	What   string
	Handle map[string]any
	Data   bool
	Why    string
}

// Owned is one thing AppTown created for a deployment at its target,
// recorded when the provider confirmed the creation. Only these are ever
// deleted at take-down: a matching name, label or mount is not evidence
// AppTown made something. Kind is the provider's word for it
// (container, network, volume, image, app, stack, bucket); ID is the
// provider's identity of it, Name a readable one when that differs.
// Data marks what holds data; Need names the need it serves, if any.
type Owned struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Data bool   `json:"data,omitempty"`
	Need string `json:"need,omitempty"`
}

// Label is how a receipt names an owned thing.
func (o Owned) Label() string {
	if o.Name != "" && o.Name != o.ID {
		return o.Kind + " " + o.Name
	}
	return o.Kind + " " + o.ID
}

// Owner records, for one deployment, each thing AppTown creates at its
// target, as soon as the provider confirms it. A driver that cannot
// record a creation stops: what is not recorded is never deleted.
type Owner interface {
	Created(ctx context.Context, o Owned) error
}

// TakeDown is what a teardown may delete. Owned is the deployment's list.
// BackedUp says a complete final backup covers every Data entry; without
// it, data is kept and reported Left. LastUser says no other deployment
// of the project shares what the project holds on this account (App
// Platform: the project's image repository).
type TakeDown struct {
	Owned    []Owned
	BackedUp bool
	LastUser bool
	// Synthetic says the deployment's data is synthetic and nothing will
	// restore it (an eval copy's): Data entries are deleted with no final
	// backup. It still deletes only what Owned names.
	Synthetic bool
}

// TakenDown is what a teardown did: what it deleted, in words for the
// receipt, and what it left (data it keeps, or a delete that failed).
type TakenDown struct {
	Deleted []string
	Left    []Left
}

// Inventorier is a driver that can say what its target holds.
type Inventorier interface {
	Inventory(ctx context.Context) ([]Resource, error)
}

// Inspector is a driver that can say whether a deployment's container is
// running as a fact, distinct from not being able to ask: absent is
// (false, nil); a host that cannot be reached is an error, and a rebuild
// stops rather than replace what it could not see.
type Inspector interface {
	Inspect(ctx context.Context, p Placement) (running bool, err error)
}
