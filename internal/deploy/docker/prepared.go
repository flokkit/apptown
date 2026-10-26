package docker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/toolversions"
)

// PreparedVersion is the prepared value's shape; a machine given another
// refuses it.
const PreparedVersion = 1

// Prepared is everything a boot reads beyond its tree and its Docker,
// resolved before the boot (design/operations/workers.md, "Previews on the worker"):
// the secrets per consumer, the needs as the services and environment that
// fulfil them, the integration environment, the browser values, the routes,
// and the one registry login. A machine that boots it reads nothing else;
// Dependencies gives a Host exactly these values.
type Prepared struct {
	Version int `json:"version"`
	// Contract is the selected contract the values were prepared for: the
	// boot runs it, and nothing else may use them (preparedSecrets).
	Contract *appcontract.Contract `json:"contract"`
	// Services replace the contract's sidecars (a workflow's services:).
	Services []deploy.ServiceSpec         `json:"services,omitempty"`
	Secrets  map[string]map[string]string `json:"secrets,omitempty"` // per consumer
	Receipts []SecretReceipt              `json:"receipts,omitempty"`
	Profile  string                       `json:"profile,omitempty"`
	Needs    []deploy.ServiceSpec         `json:"needs,omitempty"`
	NeedEnv  map[string]string            `json:"needEnv,omitempty"`
	NeedData map[string]string            `json:"needData,omitempty"`
	// Env is the attached integrations' server environment: it may hold
	// secrets, and is never public.
	Env []string `json:"env,omitempty"`
	// SecretsFingerprint identifies Secrets and Env without revealing them:
	// a keyed digest the caller that holds the key sets ("" when both are
	// empty). A secret's version does not always change with its value (an
	// Infisical value, a built-in one set again), so the values themselves
	// are what a changed secret or integration value changes, and with it
	// the preview's identity.
	SecretsFingerprint string              `json:"secretsFingerprint,omitempty"`
	BrowserEnv         string              `json:"browserEnv"`
	FrontDoor          map[string]string   `json:"frontDoor,omitempty"`
	Origins            string              `json:"origins,omitempty"`
	Registry           *RegistryCredential `json:"registry,omitempty"`
}

// RegistryCredential is the project's one registry login, for one boot.
type RegistryCredential struct {
	Host, Username, Token string
}

// PublicPrepared is the part of a prepared boot that is not secret: what a
// preview's record keeps, and what its identity is a digest of. The secret
// receipts (names, stores and versions, no values) and the fingerprint of the
// secret values and integration environment are in it, so a changed binding,
// secret or integration value is a new identity and a running preview is not
// kept with the old one.
type PublicPrepared struct {
	Version            int                   `json:"version"`
	Contract           *appcontract.Contract `json:"contract"`
	Services           []deploy.ServiceSpec  `json:"services,omitempty"`
	Receipts           []SecretReceipt       `json:"receipts,omitempty"`
	Profile            string                `json:"profile,omitempty"`
	SecretsFingerprint string                `json:"secretsFingerprint,omitempty"`
	Needs              []deploy.ServiceSpec  `json:"needs,omitempty"`
	NeedEnv            map[string]string     `json:"needEnv,omitempty"`
	NeedData           map[string]string     `json:"needData,omitempty"`
	BrowserEnv         string                `json:"browserEnv"`
	FrontDoor          map[string]string     `json:"frontDoor,omitempty"`
	Origins            string                `json:"origins,omitempty"`
}

// Public is the prepared boot without its secret values, integration
// environment or login.
func (p Prepared) Public() PublicPrepared {
	return PublicPrepared{Version: p.Version, Contract: p.Contract, Services: p.Services, Receipts: p.Receipts, Profile: p.Profile, SecretsFingerprint: p.SecretsFingerprint,
		Needs: p.Needs, NeedEnv: p.NeedEnv, NeedData: p.NeedData,
		BrowserEnv: p.BrowserEnv, FrontDoor: p.FrontDoor, Origins: p.Origins}
}

// Digest identifies the public prepared values, the contract among them:
// with the source, a preview incarnation's identity.
func (p PublicPrepared) Digest() string {
	body, _ := json.Marshal(p) // maps marshal with sorted keys
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// PrepareBoot resolves everything a boot of c from ws reads, through deps,
// for deployment ("" is preview), with the project's registry login. A need
// the catalog does not know, a required secret nothing provides, or browser
// values that cannot be read for a static site stop it here, as they would
// stop the boot.
func PrepareBoot(ctx context.Context, deps Dependencies, registry *RegistryCredential, slug, deployment, ws string, c *appcontract.Contract) (Prepared, error) {
	if err := RefusesHealth(c, false); err != nil {
		return Prepared{}, &BootError{Phase: PhaseResolve, Err: err}
	}
	if deployment == "" {
		deployment = "preview"
	}
	p := Prepared{Version: PreparedVersion, Contract: c, Registry: registry, NeedData: map[string]string{}}
	var needErr error
	p.Needs, p.NeedEnv, needErr = deps.Needs.Fulfil(ctx, c.Needs, c.NeedParams)
	if needErr != nil {
		return Prepared{}, &BootError{Phase: PhaseResolve, Err: needErr}
	}
	var sizeErr error
	p.Needs, sizeErr = sizedServices(c, p.Needs)
	if sizeErr != nil {
		return Prepared{}, &BootError{Phase: PhaseResolve, Err: sizeErr}
	}
	for _, s := range p.Needs {
		data, err := deps.Needs.Data(ctx, s.Name)
		if err != nil {
			return Prepared{}, &BootError{Phase: PhaseResolve, Err: fmt.Errorf("where %s keeps its data: %w", s.Name, err)}
		}
		p.NeedData[s.Name] = data
	}
	delivery, err := deps.Secrets.Resolve(ctx, slug, deployment, ws, c, nil)
	if err != nil {
		return Prepared{}, &BootError{Phase: PhaseSecrets, Err: err}
	}
	p.Receipts, p.Profile, p.Secrets = delivery.Receipts, delivery.Profile, map[string]map[string]string{}
	for _, part := range c.Parts {
		p.Secrets[part.Name] = delivery.EnvFor(part.Name)
	}
	for _, init := range c.Init {
		p.Secrets["init-"+init.Name] = delivery.EnvFor("init-" + init.Name)
	}
	p.BrowserEnv = "{}"
	if deps.Integrations != nil {
		if p.Env, err = deps.Integrations.EnvFor(ctx); err != nil {
			return Prepared{}, &BootError{Phase: PhaseConfig, Err: fmt.Errorf("reading what the attached integrations give the app: %w", err)}
		}
		if c.PublicPart().IsStatic() {
			if p.BrowserEnv, err = deps.Integrations.BrowserEnv(ctx); err != nil {
				return Prepared{}, &BootError{Phase: PhaseConfig, Err: fmt.Errorf("reading the values a browser may be given: %w", err)}
			}
		}
	}
	if deps.Routes != nil {
		p.FrontDoor, p.Origins = deps.Routes.FrontDoorHeaders(), deps.Routes.PublicOrigins(ctx)
	}
	return p, nil
}

// Dependencies are the prepared values as a Host's dependencies, with the
// machine's own probe and the images of one tool versions snapshot.
func (p Prepared) Dependencies(probe Prober, images toolversions.Images) Dependencies {
	return Dependencies{Secrets: preparedSecrets{p}, Needs: preparedNeeds{p}, Registry: preparedLogin{p.Registry},
		Integrations: preparedContributions{p}, Routes: preparedRoutes{p}, Probe: probe, StaticServer: images.StaticServer, ChownImage: images.Sh, DFImage: images.DF}
}

type preparedSecrets struct{ p Prepared }

// Resolve answers the prepared deliveries, only for the contract they were
// prepared for: another contract cannot use them.
func (s preparedSecrets) Resolve(ctx context.Context, _, _, _ string, c *appcontract.Contract, allowed func(context.Context) error) (Delivery, error) {
	if !SameContract(c, s.p.Contract) {
		return Delivery{}, fmt.Errorf("the boot's values were prepared for another contract")
	}
	return Delivery{Env: func(consumer string) map[string]string { return s.p.Secrets[consumer] }, Receipts: s.p.Receipts, Profile: s.p.Profile}, nil
}

// SameContract says whether two contracts declare the same thing.
func SameContract(a, b *appcontract.Contract) bool {
	if a == nil || b == nil {
		return a == b
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

type preparedNeeds struct{ p Prepared }

func (n preparedNeeds) Fulfil(context.Context, []string, map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	return n.p.Needs, n.p.NeedEnv, nil
}
func (n preparedNeeds) Data(ctx context.Context, capability string) (string, error) {
	return n.p.NeedData[capability], nil
}

type preparedContributions struct{ p Prepared }

func (c preparedContributions) EnvFor(context.Context) ([]string, error) { return c.p.Env, nil }
func (c preparedContributions) BrowserEnv(context.Context) (string, error) {
	return c.p.BrowserEnv, nil
}

type preparedRoutes struct{ p Prepared }

func (r preparedRoutes) FrontDoorHeaders() map[string]string      { return r.p.FrontDoor }
func (r preparedRoutes) PublicOrigins(ctx context.Context) string { return r.p.Origins }

// preparedLogin is the boot's one registry credential. It is written into
// the boot's own Docker configuration (WithBootConfig), never logged in
// anywhere a later command could find it.
type preparedLogin struct{ c *RegistryCredential }

func (preparedLogin) Login(context.Context, Scope, string) error { return nil }

// bootConfigPattern names a boot's Docker configuration folder under its
// scratch folder.
const bootConfigPattern = "docker-config-*"

// WithBootConfig is dk with a Docker configuration of its own, for one
// boot, in a folder under scratch (the boot's own place, which the caller
// sweeps: SweepBootConfigs). It holds the boot's one registry login, and
// nothing else: no command of the boot reads the machine's configuration or
// its credential helpers. It selects the same Docker as the ambient
// configuration: a named context in use is carried over, its records
// shared. done removes it, and answers whether it could.
//
// Docker looks for a native credential helper (a keychain) only when a
// configuration holds no credentials at all, and then keeps logins there,
// outside the folder. The configuration is therefore written with its
// credentials, never by `docker login`, and a boot without a login holds
// one entry for a host that does not exist, so nothing is looked up.
func (dk Scope) WithBootConfig(ctx context.Context, scratch string, login *RegistryCredential) (Scope, func() error, error) {
	none := func() error { return nil }
	if scratch == "" {
		return dk, none, fmt.Errorf("a boot's Docker configuration needs the boot's own folder")
	}
	current := ""
	if dk.Host == "" {
		out, err := dk.Command(ctx, Command{Args: []string{"context", "show"}})
		if err != nil {
			return dk, none, fmt.Errorf("asking Docker which context it uses: %w: %s", err, strings.TrimSpace(string(out)))
		}
		current = strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return dk, none, err
	}
	dir, err := os.MkdirTemp(scratch, bootConfigPattern)
	if err != nil {
		return dk, none, err
	}
	done := func() error { return os.RemoveAll(dir) }
	auths := map[string]map[string]string{"apptown-no-login.invalid": {}}
	if login != nil && login.Host != "" {
		auths = map[string]map[string]string{registryKey(login.Host): {
			"auth": base64.StdEncoding.EncodeToString([]byte(login.Username + ":" + login.Token))}}
	}
	config := map[string]any{"auths": auths}
	ambient, err := execution.DockerConfigDir()
	if err != nil {
		_ = done()
		return dk, none, err
	}
	// The CLI's plugins (buildx, which a static site's build needs for its
	// export) are found where the ambient configuration keeps them, as
	// before; the system's plugin folders are searched either way. Only
	// the plugins are read from there, never its credentials.
	if info, err := os.Stat(filepath.Join(ambient, "cli-plugins")); err == nil && info.IsDir() {
		config["cliPluginsExtraDirs"] = []string{filepath.Join(ambient, "cli-plugins")}
	}
	if current != "" && current != "default" {
		if err := os.Symlink(filepath.Join(ambient, "contexts"), filepath.Join(dir, "contexts")); err != nil {
			_ = done()
			return dk, none, err
		}
		config["currentContext"] = current
	}
	body, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		_ = done()
		return dk, none, err
	}
	dk.Config = dir
	return dk, done, nil
}

// registryKey is how Docker names a registry among its credentials.
func registryKey(host string) string {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return "https://index.docker.io/v1/"
	}
	return host
}

// SweepBootConfigs removes every boot configuration under scratch: those
// of boots a stop interrupted, or whose own removal failed. Its owner calls
// it only when none of its boots runs (as it starts): a configuration is
// its boot's until that boot ends.
func SweepBootConfigs(scratch string) error {
	dirs, err := filepath.Glob(filepath.Join(scratch, bootConfigPattern))
	if err != nil {
		return err
	}
	var failed []error
	for _, dir := range dirs {
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			failed = append(failed, os.RemoveAll(dir))
		}
	}
	return errors.Join(failed...)
}
