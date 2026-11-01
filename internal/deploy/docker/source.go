package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// The selected source for the built-in host: a committed tree staged so
// that nothing inside it can reach outside it, its identity as the
// artifact's bytes and hash, and its emulated services.

// ErrNoContract says the selected tree declares no hosted application.
var ErrNoContract = errors.New(".apptown/app.yaml not found — the app must declare its runtime contract")

// Selected configuration, before resolving credentials. Only this request and
// its hash are durable; a staged directory belongs to one execution.
type SourceInputs struct {
	Version  int                  `json:"version"`
	Revision string               `json:"revision"`
	Services []deploy.ServiceSpec `json:"services,omitempty"`
}

// dockerSourceServices are the emulated services the selected source
// asked for, read back from the artifact's body.
func SourceServices(art deploy.Artifact) []deploy.ServiceSpec {
	if art.Source != nil && art.Source.Services != nil {
		return append([]deploy.ServiceSpec(nil), art.Source.Services...)
	}
	var in SourceInputs
	_ = json.Unmarshal(art.Body, &in)
	return in.Services
}

func PrepareSource(ctx context.Context, repo, revision, slug string, services []deploy.ServiceSpec) (deploy.Artifact, func(), error) {
	tree, cleanup, err := (deploy.SelectedSource{Repository: repo, Revision: revision}).Stage(ctx, "")
	if err != nil {
		return deploy.Artifact{}, cleanup, err
	}
	art, err := sourceAt(tree.Directory, tree.Revision, slug, services)
	if err != nil {
		cleanup()
		return deploy.Artifact{}, func() {}, err
	}
	return art, cleanup, nil
}

func sourceAt(ws, commit, slug string, services []deploy.ServiceSpec) (deploy.Artifact, error) {
	c, err := ReadSelectedContract(ws)
	if err != nil {
		return deploy.Artifact{}, fmt.Errorf("selected Docker source requires .apptown/app.yaml: %w", err)
	}
	return SourceArtifact(deploy.Source{Workspace: ws, Revision: commit, Slug: slug, Contract: c}, services)
}

// SourceArtifact binds a declaration already read from the selected tree to its
// durable inputs. Validation and boot share this declaration; no head is reread.
func SourceArtifact(source deploy.Source, services []deploy.ServiceSpec) (deploy.Artifact, error) {
	ws, c := source.Workspace, source.Contract
	if c == nil {
		return deploy.Artifact{}, ErrNoContract
	}

	// A selected tree must be self-contained, including symlink targets. This
	// also protects contract/secret declarations before a reader follows them.
	if err := filepath.WalkDir(ws, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(ws, path)
		if err != nil {
			return err
		}
		_, err = SelectedPath(ws, rel)
		return err
	}); err != nil {
		return deploy.Artifact{}, err
	}
	for _, p := range c.Parts {
		paths := []string{p.Build, p.Static}
		if p.StaticBuild != nil {
			paths = append(paths, p.StaticBuild.Context)
		}
		for _, path := range paths {
			if path != "" {
				if _, err := SelectedPath(ws, path); err != nil {
					return deploy.Artifact{}, err
				}
			}
		}
	}
	for _, it := range c.Init {
		if it.Build != "" {
			if _, err := SelectedPath(ws, it.Build); err != nil {
				return deploy.Artifact{}, err
			}
		}
	}
	if c.SecretSpecPath != "" {
		// The declaration can be optional, but its selected path cannot escape.
		if _, err := relativePath(ws, c.SecretSpecPath); err != nil {
			return deploy.Artifact{}, err
		}
	}
	input := SourceInputs{Version: 1, Revision: source.Revision, Services: services}
	body, err := json.Marshal(input)
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &source}, nil
}

func RestoreSource(ctx context.Context, repo, slug, raw string) (deploy.Artifact, func(), error) {
	if raw == "" {
		return deploy.Artifact{}, func() {}, errors.New("deployment has no selected Docker source; inspect or deploy a fresh revision")
	}
	var in SourceInputs
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return deploy.Artifact{}, func() {}, fmt.Errorf("reading selected Docker inputs: %w", err)
	}
	if in.Version != 1 || in.Revision == "" {
		return deploy.Artifact{}, func() {}, errors.New("deployment has no supported selected Docker inputs; inspect or deploy a fresh revision")
	}
	return PrepareSource(ctx, repo, in.Revision, slug, in.Services)
}

func relativePath(root, path string) (string, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Docker source path %q must stay inside the selected tree", path)
	}
	return filepath.Join(root, clean), nil
}

// Read contract presence from selected source before deciding whether this is a
// runtime deployment. A symlink must not turn that decision into a host-file read.
func ReadSelectedContract(root string) (*appcontract.Contract, error) {
	_, err := os.Lstat(filepath.Join(root, ContractPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoContract
	}
	if err != nil {
		return nil, err
	}
	path, err := SelectedPath(root, ContractPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return appcontract.Parse(raw)
}

func SelectedPath(root, path string) (string, error) {
	full, err := relativePath(root, path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("selected source %q: %w", path, err)
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return "", err
	}
	if _, err := relativePath(base, rel); err != nil {
		return "", fmt.Errorf("selected source %q escapes through a symlink", path)
	}
	return full, nil
}

// ReadWorkspaceContract reads the native declaration in a live workspace.
// Selected immutable source continues to use ReadSelectedContract and its confinement.
func ReadWorkspaceContract(ws string) (*appcontract.Contract, error) {
	raw, err := os.ReadFile(filepath.Join(ws, ".apptown", "app.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoContract
	}
	if err != nil {
		return nil, err
	}
	return appcontract.Parse(raw)
}

// BrowserEnvVar carries a static part's browser-safe values, a JSON object.
const BrowserEnvVar = appcontract.BrowserEnvVar

// BrowserEnvScript is /_apptown/env.js around a JSON object of values. No
// literal braces: the static server's config would read them as a
// placeholder.
func BrowserEnvScript(values string) string {
	return "window.__APPTOWN_ENV__ = Object.freeze(Object.assign(new Object(), " + values + "));"
}

// StaticServerCommand serves a static part: the site from /srv, and
// /_apptown/env.js answered from BrowserEnvVar at request time, so one image
// serves every deployment its own values. A static site cannot read its
// container's environment; this file is how the attached integrations' public
// values (an error-reporting DSN, a feedback key) reach the page, as
// window.__APPTOWN_ENV__; without it a static app could adopt error reporting
// that never reports, with nothing putting the DSN in the page. Unset or
// empty, the file answers {} whatever the target set (App Platform sets
// nothing, and a bare expansion would end the script mid-expression).
// ReleasePath answers the part's release identity (ReleaseEnvVar, empty when unset): what a front
// door AppTown reaches only from outside is witnessed serving.
func StaticServerCommand(port int) []string {
	// HTTP only and HTTP/1.1 only, said outright: TLS ends at the front door,
	// so Caddy's automatic HTTPS and HTTP/2 and /3 would each log a warning at
	// every start. The file ends in a newline, as caddy fmt writes it, or Caddy
	// warns that it is not formatted.
	caddyfile := fmt.Sprintf(`{
	auto_https off
	servers {
		protocols h1
	}
}

:%d {
	@novalues vars {env.%s} ""
	handle /_apptown/env.js {
		header Content-Type "application/javascript; charset=utf-8"
		header Cache-Control "no-store"
		respond @novalues "%s"
		respond "%s"
	}
	handle %s {
		header Content-Type "text/plain; charset=utf-8"
		header Cache-Control "no-store"
		respond "{env.%s}"
	}
	handle {
		root * /srv
		file_server
	}
}
`, port, BrowserEnvVar, BrowserEnvScript(`\{\}`), BrowserEnvScript("{env."+BrowserEnvVar+"}"), appcontract.ReleasePath, appcontract.ReleaseEnvVar)
	return []string{"sh", "-c", "printf '%s' '" + caddyfile + "' > /etc/caddy/Caddyfile && exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"}
}

// StaticSiteDockerfile packs a static site's folder into the file server's
// image with the command that serves it on port (StaticServerCommand): the
// image describes itself, so every target runs it the same way and none adds
// a command (a target that ran the file server's stock configuration would
// serve its default page). The build only copies files, so
// it needs no emulation for another platform.
func StaticSiteDockerfile(server string, port int) string {
	var cmd strings.Builder
	enc := json.NewEncoder(&cmd)
	enc.SetEscapeHTML(false) // the command carries > and &, which should read as themselves
	if err := enc.Encode(StaticServerCommand(port)); err != nil {
		panic(err) // a []string always encodes
	}
	return "FROM " + server + "\nCOPY . /srv\nCMD " + strings.TrimSuffix(cmd.String(), "\n") + "\n"
}
