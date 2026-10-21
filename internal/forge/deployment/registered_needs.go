package deployment

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/integration"
)

// Needs fulfilled by registering with a shared service when an app boots
// (design/subsystems/deployment.md, "Needs fulfilled by registering"). A
// dependency kind with a register block names its adapter. One adapter runs,
// openrouter-key (the llm need): each deployment and each preview gets its
// own OpenRouter key, which it owns. The adapters below are the ones the
// project's attachment fulfils (error reporting, sign-in, telemetry) are
// integrations, not needs: declaring one is refused before this step
// (dependency.RefusesNeeds). Any other adapter refuses the boot, naming it.

// KindOpenRouterKey is how an owned list names an AI models key.
const KindOpenRouterKey = "openrouter-key"

// registeredNeed is one need the register step fulfils.
type registeredNeed struct {
	name string
	kind *dependency.Kind
}

// --- deployments --------------------------------------------------------

// deploymentKeys is the key owner for one deployment: its key is named for
// the project, the deployment and its record, and is on its owned list.
func deploymentKeys(db *database.DB, now func() time.Time, dep *Deployment, slug string) KeyOwner {
	name := fmt.Sprintf("apptown-%s-%s-%d", slug, dep.Name, dep.ID)
	return KeyOwner{
		ProjectID: dep.ProjectID,
		Name:      name,
		Scope:     fmt.Sprintf("llm:deployment:%d", dep.ID),
		Keys:      deploymentKeyRecorder{db: db, deployment: dep.ID, name: name, now: now},
	}
}

func heldKeys(owned []deploy.Owned) []integration.HeldKey {
	var out []integration.HeldKey
	for _, o := range owned {
		if o.Kind != KindOpenRouterKey {
			continue
		}
		if k, ok := integration.ParseHeldKey(o.ID); ok {
			out = append(out, k)
		}
	}
	return out
}

type deploymentKeyRecorder struct {
	db         *database.DB
	deployment ids.ID
	name       string
	now        func() time.Time // AppTown's clock
}

// Held is the keys on the deployment's owned list.
func (r deploymentKeyRecorder) Held(ctx context.Context) ([]integration.HeldKey, error) {
	owned, err := ownedIn(ctx, deploymentsql.New(r.db.SQL()), r.deployment)
	return heldKeys(owned), err
}

func (r deploymentKeyRecorder) Record(ctx context.Context, k integration.HeldKey) error {
	ctx = context.WithoutCancel(ctx) // the key exists: recorded though ctx has ended
	return recordOwned(ctx, r.db, r.deployment, deploy.Owned{Kind: KindOpenRouterKey, ID: k.Ident(), Name: r.name, Need: integration.CapLLM}, "", clock.Or(r.now)())
}

func (r deploymentKeyRecorder) Forget(ctx context.Context, k integration.HeldKey) error {
	ctx = context.WithoutCancel(ctx) // the key is gone: recorded though ctx has ended
	_, err := deploymentsql.New(r.db.SQL()).ForgetOwned(ctx, deploymentsql.ForgetOwnedParams{DeploymentID: r.deployment, Kind: KindOpenRouterKey, Ident: k.Ident()})
	return err
}

// --- what a boot is given -------------------------------------------------

// withRegistered adds the register step's values to every consumer of a
// boot's delivery, with a receipt naming them (never their values).
func withRegistered(d docker.Delivery, env map[string]string, c *appcontract.Contract) docker.Delivery {
	if len(env) == 0 {
		return d
	}
	inner := d.Env
	d.Env = func(consumer string) map[string]string {
		out := map[string]string{}
		if inner != nil {
			maps.Copy(out, inner(consumer))
		}
		maps.Copy(out, env)
		return out
	}
	consumers := consumersOf(c)
	names := slices.Sorted(maps.Keys(env))
	receipts := slices.Clone(d.Receipts)
	for _, name := range names {
		receipts = append(receipts, docker.SecretReceipt{Name: name, Store: "llm need", Version: "registered", Consumers: consumers, Generated: true})
	}
	d.Receipts = receipts
	return d
}

func consumersOf(c *appcontract.Contract) []string {
	var out []string
	if c == nil {
		return nil
	}
	for _, p := range c.Parts {
		out = append(out, p.Name)
	}
	for _, i := range c.Init {
		out = append(out, "init-"+i.Name)
	}
	return out
}

// registeredSecrets is a release's secrets with the register step's values
// given to every consumer.
type registeredSecrets struct {
	inner deploy.ConsumerSecrets
	env   map[string]string
}

func (r registeredSecrets) EnvFor(consumer string) map[string]string {
	out := map[string]string{}
	if r.inner != nil {
		maps.Copy(out, r.inner.EnvFor(consumer))
	}
	maps.Copy(out, r.env)
	return out
}

// AIModelsKey is one AI models key a deployment owns and what OpenRouter
// says of it now. Unknown says why that could not be read: an allowance
// that cannot be read is unknown, never zero.
type AIModelsKey struct {
	Name      string                 `json:"name"`
	Allowance *integration.Allowance `json:"allowance,omitempty"`
	Unknown   string                 `json:"unknown,omitempty"`
}

// AIModels reads, for someone who may read the project's deployments, each
// AI models key the deployment owns and its allowance and spend at
// OpenRouter now. A deployment without one answers an empty list.
func (s *Service) AIModels(ctx context.Context, projectID ids.ID, name string) ([]AIModelsKey, error) {
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.ai-models"}}); err != nil {
		return nil, err
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	owned, err := s.Deployments.Owned(ctx, dep.ID)
	if err != nil {
		return nil, err
	}
	out := []AIModelsKey{}
	for _, o := range owned {
		if o.Kind != KindOpenRouterKey {
			continue
		}
		k := AIModelsKey{Name: o.Name}
		held, ok := integration.ParseHeldKey(o.ID)
		switch {
		case !ok:
			k.Unknown = "the record does not name a key"
		case s.env.integrations == nil:
			k.Unknown = "this AppTown has no integrations to ask OpenRouter with"
		default:
			a, err := s.env.integrations.LLMAllowance(ctx, held)
			if err != nil {
				k.Unknown = err.Error()
			} else {
				k.Allowance = &a
			}
		}
		out = append(out, k)
	}
	return out, nil
}
