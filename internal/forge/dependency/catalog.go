// Package dependency owns the dependency catalog (design/architecture/
// contracts.md): the kinds an app may declare it needs (`x-forge-needs:
// [s3, postgres]`), each with the local emulator, the cloud template and
// the register connector that can fulfil it. Stock kinds ship in
// stock/dependencies/*.yaml and sync in at boot; custom kinds are saved
// through the API and never overwrite a stock one. The catalog resolves local
// requirements; consumers render them as containers, compose or process env.
package dependency

import (
	"context"
	"database/sql"
	"errors"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dependency/internal/dependencysql"
)

// Origin says who wrote a kind: the platform (stock) or a person (custom).
// Stock kinds are refreshed at boot and are read-only through the API.
type Origin uint8

const (
	Stock Origin = iota + 1
	Custom
)

var ErrOrigin = errors.New("invalid dependency origin")

func (o Origin) String() string {
	switch o {
	case Stock:
		return "stock"
	case Custom:
		return ""
	}
	return "invalid-origin"
}

func ParseOrigin(text string) (Origin, error) {
	switch text {
	case "stock":
		return Stock, nil
	case "":
		return Custom, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrOrigin, text)
}

// Kind is one catalogued dependency.
type Kind struct {
	Sizes       map[string]Size           `yaml:"sizes,omitempty" json:"sizes,omitempty"`
	Resources   appcontract.MemoryDefault `yaml:"resources,omitempty" json:"resources,omitzero"`
	Capability  string                    `yaml:"capability" json:"capability"`
	Title       string                    `yaml:"title" json:"title"`
	Icon        string                    `yaml:"icon,omitempty" json:"icon,omitempty"`
	Description string                    `yaml:"description,omitempty" json:"description,omitempty"`
	// Provides is the kind's contract with an app: every variable the app is
	// given, by name, with what it means. It is the same on every target
	// that fulfils the kind; a target expresses the values its own way (an
	// emulator's inject, a managed binding) and gives exactly these names.
	// A kind that declares it is held to it: its inject names are exactly
	// these. No two kinds provide the same name.
	Provides map[string]string `yaml:"provides,omitempty" json:"provides,omitempty"`
	// Defaults are the parameter values assumed when the app declares the need
	// bare (`postgres`) or omits a param. The app's declared params override
	// these; both are interpolated as {name} tokens into the fulfillments
	// (Tier-1 passthrough).
	Defaults map[string]string `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	// Credentials are values the forge sets and an app may not: interpolated
	// as {name} like a parameter, after the app's parameters, and refused as a
	// parameter (RefusesParams). A managed target makes its own, so an app
	// that chose one would rely on it only where it runs locally.
	Credentials map[string]string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	// Generate names the credentials each deployment and each preview gets
	// a value of its own for (Credentials, LocalRuntime.Credentials); the
	// value under credentials: is then only for the forge's own throwaway
	// servers (steps, forge dev, checks). A kind with a local service that
	// generates one declares local.configAtStart: its service must take a
	// new value at its next start, never keep the first one in its data.
	Generate []string `yaml:"generate,omitempty" json:"generate,omitempty"`
	// Integration marks a kind whose values come from the project's attached
	// integration (error reporting, sign-in, telemetry): the list of what an
	// attached integration delivers, never declared as a need. The app must
	// run without it.
	Integration bool `yaml:"integration,omitempty" json:"integration,omitempty"`
	Local       struct {
		Image   string   `yaml:"image" json:"image"`
		Command []string `yaml:"command,omitempty" json:"command,omitempty"`
		// Entrypoint replaces the image's entrypoint: a start script that
		// readies the service for the app before it marks itself healthy.
		Entrypoint []string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
		// ConfigAtStart says the service sets everything it is configured
		// with again at every start and keeps none of it only in its data,
		// so a standing one whose configuration changed is remade on its
		// own volume (deploy.ServiceSpec.ConfigAtStart). A database, which
		// bakes its first user and password into its files, must not.
		ConfigAtStart bool `yaml:"configAtStart,omitempty" json:"configAtStart,omitempty"`
		// KeepsImage says the service's data is in its image's own format
		// (a database's files, which another major version cannot read): a
		// standing one whose image moved is not remade on its volume under
		// ConfigAtStart, and takes the rule for any other data service
		// (deploy.ServiceSpec.KeepsImage).
		KeepsImage bool `yaml:"keepsImage,omitempty" json:"keepsImage,omitempty"`
		// StepEnv is added to Env for the servers the forge starts beside
		// its own steps and for forge dev, where the app's own tests run
		// (LocalRuntime.Steps); never for a deployment or a preview.
		StepEnv map[string]string `yaml:"stepEnv,omitempty" json:"stepEnv,omitempty"`
		Port    int               `yaml:"port,omitempty" json:"port,omitempty"`
		Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
		Inject  map[string]string `yaml:"inject,omitempty" json:"inject,omitempty"`
		// Health is a shell healthcheck (exit 0 = ready), e.g. pg_isready. It
		// lets the runtime and generated compose gate the app on the emulator
		// being READY, not a blind sleep.
		Health string `yaml:"health,omitempty" json:"health,omitempty"`
		// Data is the emulator's data directory. Persistent tiers (the
		// intranet) keep it in a named volume so the need's data survives
		// releases — a database that forgets everything on every release
		// is not a database. Empty = ephemeral is fine (dynamodb-local).
		Data string `yaml:"data,omitempty" json:"data,omitempty"`
	} `yaml:"local" json:"local"`
	// Copy is how this kind's data is copied, declared once wherever it
	// runs (design/subsystems/deployment.md, "Backups and restore"): the
	// commands that export it, put it back in place, fill a new, empty
	// place from it and verify it. The commands read the kind's standard
	// connection variables, so the same text runs inside the need's own
	// container on a Docker host and from the copy runner for a managed
	// instance. nil, or a copy with no export: the forge copies the
	// kind's data directory as files.
	Copy *Copy `yaml:"copy,omitempty" json:"copy,omitempty"`
	// Cloud fulfillment per provider (digitalocean, aws, …): how the
	// provider makes the need (kind: managed-database, or a terraform
	// template ref, not executed yet), how the forge reaches an instance for
	// a copy, and the provider's own protection, if it has any.
	Cloud map[string]Cloud `yaml:"cloud,omitempty" json:"cloud,omitempty"`
	// Register fulfillment: the need is satisfied by REGISTERING with a shared
	// org singleton (SSO/Authentik, error reporting) rather than provisioning a
	// per-app instance — the connector + the provision adapter that mints the
	// client/key, and the env it yields. Parsed and catalog-visible so the
	// unified catalog is honest (design/subsystems/deployment.md, "Needs
	// fulfilled by registering").
	Register *struct {
		Connector string            `yaml:"connector" json:"connector"`
		Adapter   string            `yaml:"adapter,omitempty" json:"adapter,omitempty"`
		Inject    map[string]string `yaml:"inject,omitempty" json:"inject,omitempty"`
	} `yaml:"register,omitempty" json:"register,omitempty"`
}

// Copy is a kind's copy. Export writes the copy to stdout; Verify reads
// it from stdin and exits 0 only when the place holds it. Restore puts it
// back in place, over what is there; Import fills a new, empty place from
// it. The two stay apart: a restore keeps the place, an import makes
// nothing but what the copy holds. On a Docker host the commands run in
// the need's own container, given the connection it was started with; for
// a managed instance, in a throwaway container of Client, as the app's own
// user.
type Copy struct {
	// Format names the representation of a copy that fills a new place: it
	// is filled only into a kind that reads the same format. Declared with
	// Import and Client, never without.
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Ext    string `yaml:"ext,omitempty" json:"ext,omitempty"`
	// Client is the image the commands run in away from the need's own
	// container; {version} is the need's.
	Client string `yaml:"client,omitempty" json:"client,omitempty"`
	// RunIn says where the commands run beside a running need: "service"
	// (the default) in the need's own container; "client" in a throwaway
	// container of Client that joins the need container's network, for a
	// service whose image has no copy tool (an S3 server). Either way the
	// connection's host is the need's own 127.0.0.1.
	RunIn  string `yaml:"runIn,omitempty" json:"runIn,omitempty"`
	Export string `yaml:"export,omitempty" json:"export,omitempty"`
	Import string `yaml:"import,omitempty" json:"import,omitempty"`
	// Restore puts a copy back into the need's own place, in its running
	// service. A kind with local data declares it with its export.
	Restore string `yaml:"restore,omitempty" json:"restore,omitempty"`
	// RestoreStopped runs Restore while the need's container is stopped,
	// in a throwaway container of the same image on the same volumes, then
	// starts the need again: for a service that reads its files only when
	// it starts (Redis and dump.rdb).
	RestoreStopped bool   `yaml:"restoreStopped,omitempty" json:"restoreStopped,omitempty"`
	Verify         string `yaml:"verify,omitempty" json:"verify,omitempty"`
	// Caveat is what a copy does not hold, in words for the person
	// deciding on a restore (object metadata a file copy leaves out). It
	// travels with each copy and each restore of it.
	Caveat string `yaml:"caveat,omitempty" json:"caveat,omitempty"`
	// Empty exits 0 only when the database the connection names holds no
	// data of anyone's: before a take-down deletes an instance, every
	// database on it that no copy covers must answer empty, whatever its
	// name (a provider's default database is connectable and writable).
	Empty string `yaml:"empty,omitempty" json:"empty,omitempty"`
	// Env is the connection a copy job is given, as the kind's standard
	// variables, each from the tokens {host}, {port}, {database}, {user},
	// {password} and {sslmode} of the app user's connection.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// Where a copy's commands run beside a running need (Copy.RunIn).
const (
	RunInService = "service"
	RunInClient  = "client"
)

// InClient says the commands run in a client container beside the need.
func (c *Copy) InClient() bool { return c != nil && c.RunIn == RunInClient }

// CopyTokens are the parts of a connection a copy's Env may name.
var CopyTokens = []string{"host", "port", "database", "user", "password", "sslmode"}

var tokenRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// Cloud is a kind on one provider.
type Cloud struct {
	// Kind is the provider's need module that makes it (managed-database,
	// bucket).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Reach is how the forge reaches an instance on this provider for a
	// copy; nil: it does not, and the instance's data is not protected by
	// the forge there.
	Reach *Reach `yaml:"reach,omitempty" json:"reach,omitempty"`
	// Native is the provider's own protection of the instance, which the
	// forge reads (never restores from) and shows beside its own copies.
	Native *Native `yaml:"native,omitempty" json:"native,omitempty"`
}

// Reach says how the forge reaches an instance to copy it.
type Reach struct {
	// How: connect runs a throwaway container of the copy's client image,
	// given the instance's connection.
	How string `yaml:"how" json:"how"`
	// From is where that container runs: named-host is the Docker host
	// named when the target was attached (its copy runner).
	From string `yaml:"from" json:"from"`
	// Credential is whose connection it is given: app-user is the app's
	// own user, read from the provider at job time and never recorded.
	Credential string `yaml:"credential" json:"credential"`
}

// Native names the provider's own protection the forge reads.
type Native struct {
	Mechanism string `yaml:"mechanism" json:"mechanism"`
}

// The reaches, places and credentials the engine implements. A declaration
// names one of them; it cannot add one, nor grant itself another
// credential.
const (
	ReachConnect        = "connect"
	FromNamedHost       = "named-host"
	CredentialAppUser   = "app-user"
	NativeDOBackups     = "do-database-backups"
	ManagedDatabaseKind = "managed-database"
	// BucketKind is a provider's object storage bucket made for one
	// deployment (App Platform: a Spaces bucket and a key for it alone).
	BucketKind = "bucket"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var (
	ErrSpec    = fault.New(fault.Invalid, "invalid dependency kind")
	ErrStock   = fault.New(fault.Conflict, "stock dependency")
	ErrUnknown = errors.New("unknown dependency")
)

// Parse reads a kind's YAML and applies the two rules: the capability is
// a slug, and the kind is fulfilled by PROVISION (a local emulator) or by
// REGISTER (a shared org connector), at least one.
func Parse(y string) (*Kind, error) {
	var d Kind
	if err := yaml.Unmarshal([]byte(y), &d); err != nil {
		return nil, fmt.Errorf("%w: yaml: %w", ErrSpec, err)
	}
	if !slugRe.MatchString(d.Capability) {
		return nil, fmt.Errorf("%w: capability must be a slug, got %q", ErrSpec, d.Capability)
	}
	if d.Local.Image == "" && d.Register == nil {
		return nil, fmt.Errorf("%w: dependency %q needs a local.image emulator (PROVISION) or a register block (REGISTER)", ErrSpec, d.Capability)
	}
	// A kind's copy is declared under copy:, never local.backup. That block
	// is refused by name rather than ignored: ignored, it would turn a
	// database's consistent copy into a tar of its live files.
	var legacy struct {
		Local struct {
			Backup any `yaml:"backup"`
		} `yaml:"local"`
	}
	if err := yaml.Unmarshal([]byte(y), &legacy); err == nil && legacy.Local.Backup != nil {
		return nil, fmt.Errorf("%w: dependency %q declares local.backup, which is now copy: (export, restore, restoreStopped, verify, ext, caveat)", ErrSpec, d.Capability)
	}
	if err := d.checkSizes(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSpec, err)
	}
	if err := d.checkProvides(); err != nil {
		return nil, err
	}
	if err := d.checkGenerate(); err != nil {
		return nil, err
	}
	if err := d.checkCopy(); err != nil {
		return nil, err
	}
	return &d, nil
}

// checkProvides holds what a kind's fulfilments inject to its declared
// contract: a name injected but not provided would reach an app on one
// target only, and a name provided but not injected would be promised and
// missing.
func (k *Kind) checkProvides() error {
	if len(k.Provides) == 0 {
		return nil
	}
	check := func(where string, inject map[string]string) error {
		var extra, missing []string
		for name := range inject {
			if _, ok := k.Provides[name]; !ok {
				extra = append(extra, name)
			}
		}
		for name := range k.Provides {
			if _, ok := inject[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(extra)+len(missing) == 0 {
			return nil
		}
		slices.Sort(extra)
		slices.Sort(missing)
		var why []string
		if len(extra) > 0 {
			why = append(why, "injects "+strings.Join(extra, ", ")+", which it does not provide")
		}
		if len(missing) > 0 {
			why = append(why, "does not inject "+strings.Join(missing, ", ")+", which it provides")
		}
		return fmt.Errorf("%w: dependency %q: its %s %s", ErrSpec, k.Capability, where, strings.Join(why, "; "))
	}
	if k.Local.Image != "" {
		if err := check("local fulfilment", k.Local.Inject); err != nil {
			return err
		}
	}
	if k.Register != nil {
		if err := check("registration", k.Register.Inject); err != nil {
			return err
		}
	}
	return nil
}

// checkGenerate holds generate: to the credentials it names and to a
// service that sets them again at every start: one that kept its first
// value in its data would refuse every later deploy.
func (k *Kind) checkGenerate() error {
	for _, name := range k.Generate {
		if _, ok := k.Credentials[name]; !ok {
			return fmt.Errorf("%w: dependency %q generates %q, which is not one of its credentials", ErrSpec, k.Capability, name)
		}
	}
	if len(k.Generate) > 0 && k.Local.Image != "" && !k.Local.ConfigAtStart {
		return fmt.Errorf("%w: dependency %q generates credentials for each deployment, so its local service must set them again at every start (local.configAtStart: true)", ErrSpec, k.Capability)
	}
	return nil
}

// Names are the variables the kind gives an app: what it provides, or,
// for a kind that declares no contract, what it injects.
func (k *Kind) Names() []string {
	set := map[string]bool{}
	for name := range k.Provides {
		set[name] = true
	}
	if len(set) == 0 {
		for name := range k.Local.Inject {
			set[name] = true
		}
		if k.Register != nil {
			for name := range k.Register.Inject {
				set[name] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// checkCopy holds the portable copy and each provider's reach to what the
// engine can do: a copy declares every command (an export with no verify
// would be a copy nothing ever confirms comes back), and a reach or a
// native mechanism is one the engine implements.
func (k *Kind) checkCopy() error {
	if c := k.Copy; c != nil {
		bad := func(why string) error {
			return fmt.Errorf("%w: dependency %q declares a copy %s", ErrSpec, k.Capability, why)
		}
		blank := func(v string) bool { return strings.TrimSpace(v) == "" }
		switch {
		case blank(c.Export):
			// No export: the forge copies the data directory as files, and
			// the kind may say only what that copy leaves out.
			if !blank(c.Format+c.Ext+c.Client+c.Import+c.Restore+c.Verify+c.Empty) || c.RestoreStopped || len(c.Env) > 0 {
				return bad("with no export: a copy without one is AppTown's file copy of the data directory and may declare only its caveat")
			}
		case blank(c.Ext) || blank(c.Verify):
			// An export nothing ever confirms came back is not a copy.
			return bad("without ext and verify")
		case blank(c.Restore) && k.Local.Data != "":
			// A Docker host's copy goes back in place: an export with no
			// restore could never be put back.
			return bad("with no restore, and the kind keeps local data at " + k.Local.Data)
		}
		// A copy that fills a new place says what it is (format), how
		// (import) and where that runs (client): all three or none.
		declared := 0
		for _, v := range []string{c.Format, c.Import, c.Client} {
			if !blank(v) {
				declared++
			}
		}
		if declared != 0 && declared != 3 {
			return bad("that fills a new place without all of format, import and client")
		}
		switch c.RunIn {
		case "", RunInService:
		case RunInClient:
			if blank(c.Client) {
				return bad("that runs in a client container with no client image")
			}
		default:
			return bad(fmt.Sprintf("that runs in %q; a copy runs in its %s or a %s container", c.RunIn, RunInService, RunInClient))
		}
		if c.Format != "" && !slugRe.MatchString(c.Format) {
			return fmt.Errorf("%w: dependency %q: copy format must be a slug, got %q", ErrSpec, k.Capability, c.Format)
		}
		for name, value := range c.Env {
			for _, m := range tokenRe.FindAllStringSubmatch(value, -1) {
				if !slices.Contains(CopyTokens, m[1]) {
					return fmt.Errorf("%w: dependency %q: copy env %s names {%s}; a connection has %s", ErrSpec, k.Capability, name, m[1], strings.Join(CopyTokens, ", "))
				}
			}
		}
	}
	for provider, cloud := range k.Cloud {
		if r := cloud.Reach; r != nil {
			if k.Copy == nil || k.Copy.Import == "" || len(k.Copy.Env) == 0 {
				return fmt.Errorf("%w: dependency %q says how to reach it on %s for a copy but declares no copy that fills a new place (copy.format, import, client), with the connection it is given (copy.env)", ErrSpec, k.Capability, provider)
			}
			// A cluster is deleted only when every database on it no copy
			// covers says it holds nothing: a managed database declares that
			// check. A bucket is the deployment's alone.
			if cloud.Kind == ManagedDatabaseKind && strings.TrimSpace(k.Copy.Empty) == "" {
				return fmt.Errorf("%w: dependency %q on %s: a %s reached for a copy declares the check that a database holds nothing (copy.empty)", ErrSpec, k.Capability, provider, ManagedDatabaseKind)
			}
			if r.How != ReachConnect || r.From != FromNamedHost || r.Credential != CredentialAppUser {
				return fmt.Errorf("%w: dependency %q on %s: AppTown reaches a cloud instance by connect, from the named host, as the app's user (how: %s, from: %s, credential: %s), got how %q, from %q, credential %q",
					ErrSpec, k.Capability, provider, ReachConnect, FromNamedHost, CredentialAppUser, r.How, r.From, r.Credential)
			}
			if cloud.Kind != ManagedDatabaseKind && cloud.Kind != BucketKind {
				return fmt.Errorf("%w: dependency %q on %s: only a %s or a %s is reached for a copy, not %q", ErrSpec, k.Capability, provider, ManagedDatabaseKind, BucketKind, cloud.Kind)
			}
		}
		if n := cloud.Native; n != nil && !(provider == "digitalocean" && n.Mechanism == NativeDOBackups) {
			return fmt.Errorf("%w: dependency %q on %s: AppTown reads no native protection called %q there", ErrSpec, k.Capability, provider, n.Mechanism)
		}
	}
	return nil
}

// Catalog is the supported API over dependency_kinds.
type Catalog struct {
	db    *database.DB
	clock func() time.Time
}

// NewCatalog is the catalog over db; now is the forge's clock, which stamps
// each kind's update and receipt (the real clock when nil).
func NewCatalog(db *database.DB, now func() time.Time) *Catalog { return &Catalog{db: db, clock: now} }

func (c *Catalog) now() time.Time { return clock.Or(c.clock)().UTC() }

// ErrStockUnreadable names shipped kind files that did not parse.
var ErrStockUnreadable = errors.New("stock dependency kinds that do not parse")

// SyncStock upserts every *.yaml under dir in fsys as a stock kind, named
// by its file (redis.yaml is the redis kind). A file that does not parse,
// or names another capability, never breaks startup, but it is not quietly
// passed over either: its bytes replace the stored stock kind of its name,
// so every reader of that kind gets the parse error with its reason (a
// backup says the need's data was not copied, a release refuses the need)
// instead of the previous release's definition; and the returned error,
// ErrStockUnreadable, names each such file and why. skipped lists them as
// "file: reason".
//
// A stock kind the release no longer ships is removed, so it is no longer
// catalogued: an app that still declares it is refused as an unknown need,
// rather than fulfilled from the definition of a release that withdrew it
// (dynamodb, before 1.0). Custom kinds are never touched.
func (c *Catalog) SyncStock(ctx context.Context, fsys fs.FS, dir string) (synced []string, skipped []string, err error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, nil, err
	}
	var shipped []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			shipped = append(shipped, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	if _, err := queries(c.db).DeleteStockDependencyKindsNotShipped(ctx, dependencysql.DeleteStockDependencyKindsNotShippedParams{Origin: Stock.String(), Shipped: shipped}); err != nil {
		return nil, nil, fmt.Errorf("removing the stock dependency kinds this release no longer ships: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		raw, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			skipped = append(skipped, e.Name()+": "+err.Error())
			continue
		}
		d, perr := Parse(string(raw))
		if perr == nil && d.Capability != name {
			perr = fmt.Errorf("%w: the file %s declares the capability %q; a stock kind is named by its file", ErrSpec, e.Name(), d.Capability)
		}
		if perr != nil {
			skipped = append(skipped, e.Name()+": "+perr.Error())
			if !slugRe.MatchString(name) {
				continue
			}
			// The shipped file is stored as it is, unreadable, in place of
			// whatever the stock kind was before.
			if err := queries(c.db).SyncStockDependencyKind(ctx, dependencysql.SyncStockDependencyKindParams{
				Capability: name, SpecYaml: string(raw), Origin: Stock.String(), UpdatedAt: c.now(),
			}); err != nil {
				return synced, skipped, fmt.Errorf("recording the unreadable stock dependency kind %s: %w", name, err)
			}
			continue
		}
		if err := queries(c.db).SyncStockDependencyKind(ctx, dependencysql.SyncStockDependencyKindParams{
			Capability: d.Capability, SpecYaml: string(raw), Origin: Stock.String(), UpdatedAt: c.now(),
		}); err != nil {
			return synced, skipped, fmt.Errorf("syncing the stock dependency kind %s: %w", d.Capability, err)
		}
		synced = append(synced, d.Capability)
	}
	if len(skipped) > 0 {
		return synced, skipped, fmt.Errorf("%w: %s", ErrStockUnreadable, strings.Join(skipped, "; "))
	}
	return synced, skipped, nil
}

// ByCapability is one kind, ErrUnknown when it is not catalogued.
func (c *Catalog) ByCapability(ctx context.Context, capability string) (*Kind, error) {
	r, err := queries(c.db).DependencyKindByCapability(ctx, capability)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, capability)
	}
	if err != nil {
		return nil, err
	}
	return parseStored(r.Capability, r.SpecYaml)
}

// parseStored reads a stored kind and holds it to the name it is stored
// under: a definition stored as one capability that declares another (a
// shipped file named for one kind holding another's) is unreadable under
// both names, never served as the other kind.
func parseStored(capability, spec string) (*Kind, error) {
	d, err := Parse(spec)
	if err != nil {
		return nil, err
	}
	if d.Capability != capability {
		return nil, fmt.Errorf("%w: the kind stored as %q declares the capability %q", ErrSpec, capability, d.Capability)
	}
	return d, nil
}

// All is the catalog in capability order; a stored kind that no longer
// parses is left out rather than failing the whole list.
func (c *Catalog) All(ctx context.Context) ([]*Kind, error) {
	rows, err := queries(c.db).DependencyKinds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Kind, 0, len(rows))
	for _, row := range rows {
		if kind, err := parseStored(row.Capability, row.SpecYaml); err == nil {
			out = append(out, kind)
		}
	}
	return out, nil
}

// Save adds or updates a custom kind from its YAML, with the receipt. A
// stock kind is ErrStock: clone it under a new capability to customise.
func (c *Catalog) Save(ctx context.Context, y, who string) (*Kind, error) {
	d, err := Parse(y)
	if err != nil {
		return nil, err
	}
	now := c.now()
	err = c.db.InTx(ctx, func(tx *database.Tx) error {
		row, err := queries(tx).DependencyKindByCapability(ctx, d.Capability)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			origin, parseErr := ParseOrigin(row.Origin)
			if parseErr != nil {
				return parseErr
			}
			if origin == Stock {
				return fmt.Errorf("%w: %q is a stock dependency — clone it under a new capability", ErrStock, d.Capability)
			}
		}
		if _, err := queries(tx).SaveCustomDependencyKind(ctx, dependencysql.SaveCustomDependencyKindParams{
			Capability: d.Capability, SpecYaml: y, Origin: Custom.String(), UpdatedAt: now,
		}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "dependency.saved", d.Capability, nil)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}
