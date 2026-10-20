package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"
)

// AttachTarget: POST /targets — add a deployment target. Kinds: a
// Docker host over SSH {kind: docker-host, name, host, keyFrom, domain}; a
// Portainer environment {kind: portainer, name, urlFrom, apiKeyFrom,
// environment, domain}; a bucket website {kind: s3-bucket, name, endpoint,
// region, websiteEndpoint, accessKeyFrom, secretKeyFrom}; a DigitalOcean account's App Platform {kind:
// do-app, name, tokenFrom, region, registry?} — the account connector is
// made here too, so the cloud account is one door, not an integration
// apart. Every kind must answer before it is saved.
func (h Targets) AttachTarget(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Host        string `json:"host"`
		KeyFrom     string `json:"keyFrom"`
		URLFrom     string `json:"urlFrom"`
		APIKeyFrom  string `json:"apiKeyFrom"`
		Environment string `json:"environment"`
		Domain      string `json:"domain"`
		TokenFrom   string `json:"tokenFrom"`
		Region      string `json:"region"`
		Registry    string `json:"registry"`
		// A bucket target: the store's API endpoint, the website address
		// template ({bucket}, {region}) and the secrets holding its key pair.
		Endpoint        string `json:"endpoint"`
		WebsiteEndpoint string `json:"websiteEndpoint"`
		AccessKeyFrom   string `json:"accessKeyFrom"`
		SecretKeyFrom   string `json:"secretKeyFrom"`
		// DryRun: ask the target to answer and report, save nothing — the
		// wizard's connection step, before the person commits.
		DryRun bool `json:"dryRun"`
		// Evals keeps an attached Docker host for evals: its domain is the
		// eval domain, and it takes eval copies only.
		Evals bool `json:"evals"`
	}) {
		in.Name = strings.TrimSpace(in.Name)
		if !project.ValidSlug(in.Name) {
			WriteError(w, 400, "the name must be a slug: lowercase letters, digits and dashes")
			return
		}
		if existing, err := h.deployment.Targets.ByName(r.Context(), in.Name); err == nil && !in.DryRun {
			// A retired target keeps its name with its history; nothing
			// revives it, so the next action is a new name.
			if errors.Is(existing.Status.Available(), deployment.ErrTargetRetired) {
				WriteError(w, 409, "a target named %q was retired; its name stays with its history, so attach this one under a new name", in.Name)
				return
			}
			WriteError(w, 409, "a target named %q already exists", in.Name)
			return
		}
		in.Domain = strings.TrimSpace(strings.TrimPrefix(in.Domain, "*."))
		if in.Domain != "" {
			if _, err := url.Parse("https://" + in.Domain); err != nil || strings.ContainsAny(in.Domain, "/ ") {
				WriteError(w, 400, "the domain must be a bare hostname, such as apps.example.com")
				return
			}
		}
		if in.Kind == "" {
			in.Kind = deployment.KindDockerHost
		}
		if in.Evals && (in.Kind != deployment.KindDockerHost || in.Domain == "") {
			WriteError(w, 400, "only a Docker host with a domain can be kept for evals: its domain is the eval domain")
			return
		}
		var t *deployment.Target
		var detail string
		switch in.Kind {
		case deployment.KindDockerHost:
			addr, err := deployment.ParseSSHAddress(in.Host)
			if err != nil {
				WriteError(w, 400, "%v", err)
				return
			}
			in.KeyFrom = strings.TrimSpace(in.KeyFrom)
			if in.KeyFrom == "" {
				WriteError(w, 400, "keyFrom names the secret holding the SSH private key")
				return
			}
			ok, words := h.hosts.ProbeHostTarget(r.Context(), in.Name, addr, in.KeyFrom)
			if !ok {
				WriteError(w, 422, "not attached — %s", words)
				return
			}
			detail = words
			t = &deployment.Target{
				Name: in.Name, Kind: deployment.KindDockerHost, Provider: "", Origin: deployment.OriginAttached,
				Address:      map[string]any{"host": in.Host, "keyFrom": in.KeyFrom, "user": addr.User, "hostname": addr.Host, "port": addr.Port},
				Capabilities: deployment.TargetCapabilities{PersistentVolumes: true, Previews: false, OwnLiveness: false, Rollout: []string{"replace"}, Needs: h.needs.EmulatorKinds(r.Context())},
				Ingress:      map[string]any{},
				NeedsMode:    "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady,
			}
		case deployment.KindPortainer:
			in.URLFrom, in.APIKeyFrom, in.Environment = strings.TrimSpace(in.URLFrom), strings.TrimSpace(in.APIKeyFrom), strings.TrimSpace(in.Environment)
			if in.URLFrom == "" || in.APIKeyFrom == "" || in.Environment == "" {
				WriteError(w, 400, "a Portainer target needs urlFrom and apiKeyFrom (secret names) and the Portainer environment's name")
				return
			}
			probe := &deployment.Target{Name: in.Name, Kind: deployment.KindPortainer, Address: map[string]any{"urlFrom": in.URLFrom, "apiKeyFrom": in.APIKeyFrom, "environment": in.Environment}, Ingress: map[string]any{"domain": in.Domain}}
			cfg, err := h.providers.PortainerConfig(r.Context(), probe)
			if err != nil {
				WriteError(w, 422, "not attached — %v", err)
				return
			}
			id, err := portainer.EndpointID(r.Context(), cfg)
			if err != nil {
				WriteError(w, 422, "not attached — Portainer did not answer as expected: %s", strs.Truncate(err.Error(), 200))
				return
			}
			detail = fmt.Sprintf("Portainer answers; environment %s is endpoint %d (%s)", cfg.EnvName, id, portainer.PlatformText(r.Context(), cfg, id))
			t = &deployment.Target{
				Name: in.Name, Kind: deployment.KindPortainer, Provider: "", Origin: deployment.OriginAttached,
				Address:      map[string]any{"urlFrom": in.URLFrom, "apiKeyFrom": in.APIKeyFrom, "environment": in.Environment, "endpoint": id},
				Capabilities: deployment.TargetCapabilities{PersistentVolumes: true, Previews: false, OwnLiveness: true, Rollout: []string{"replace"}, Needs: h.needs.EmulatorKinds(r.Context())},
				Ingress:      map[string]any{"binding": "traefik"},
				NeedsMode:    "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady,
			}
		case deployment.KindDOApp:
			in.TokenFrom, in.Region = strings.TrimSpace(in.TokenFrom), strings.TrimSpace(in.Region)
			if in.TokenFrom == "" || in.Region == "" {
				WriteError(w, 400, "a DigitalOcean target needs tokenFrom (the secret holding an API token) and a region")
				return
			}
			cfg := map[string]any{"tokenFrom": in.TokenFrom, "region": in.Region}
			if in.Registry = strings.TrimSpace(in.Registry); in.Registry != "" {
				cfg["registry"] = in.Registry
			}
			health := h.providers.CheckCloudAccount(r.Context(), cfg)
			if !health.OK {
				WriteError(w, 422, "not attached — %s", health.Detail)
				return
			}
			detail = health.Detail
			// The registry is a fact of the account, not a choice: report it, and
			// only when the account has none does the name to create matter.
			reading, err := h.providers.DigitalOceanRegistry(r.Context(), digitalocean.Config{TokenFrom: in.TokenFrom})
			if err == nil && reading.Unknown != "" && reading.Registry == "" {
				err = errors.New(reading.Unknown)
			}
			if err != nil {
				WriteError(w, 422, "not attached — asking about the container registry: %v", err)
				return
			}
			registry := reading.Registry
			if registry != "" {
				detail += ", " + reading.Words()
			} else if in.Registry != "" {
				detail += ", container registry " + in.Registry + " to be created on the first release"
			} else {
				detail += ", no container registry yet"
			}
			if in.DryRun {
				WriteJSON(w, 200, CloudAccountCheck{OK: true, Detail: detail, DryRun: true, Registry: registry})
				return
			}
			// Unmanaged: nothing in apptown config declares this account, so it is
			// the target's to keep and to take away with it when retired.
			h.bindCloudAccount(r.Context(), in.Name, cfg)
			audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "integration.bound", in.Name, map[string]any{"capability": digitalocean.CapCloudAccount, "provider": "digitalocean", "via": "targets"})
			saved, err := h.deployment.SeedCloudTarget(r.Context(), in.Name, Actor(r))
			if err != nil {
				WriteFault(w, err)
				return
			}
			audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "target.attached", saved.Name, map[string]any{"kind": saved.Kind, "region": in.Region, "tokenFrom": in.TokenFrom, "answered": detail})
			audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "target.checked", saved.Name, map[string]any{"ok": true, "detail": detail, "registry": reading})
			WriteJSON(w, 200, CloudTargetAttached{OK: true, Target: saved, Detail: detail, Registry: registry})
			return
		case deployment.KindS3Bucket:
			address := map[string]any{"endpoint": strings.TrimRight(strings.TrimSpace(in.Endpoint), "/"), "region": strings.TrimSpace(in.Region), "websiteEndpoint": strings.TrimSpace(in.WebsiteEndpoint),
				"accessKeyFrom": strings.TrimSpace(in.AccessKeyFrom), "secretKeyFrom": strings.TrimSpace(in.SecretKeyFrom)}
			for _, k := range []string{"endpoint", "region", "websiteEndpoint", "accessKeyFrom", "secretKeyFrom"} {
				if address[k] == "" {
					WriteError(w, 400, "a bucket target needs endpoint, region, websiteEndpoint (with {bucket}), accessKeyFrom and secretKeyFrom (secret names)")
					return
				}
			}
			if !strings.Contains(address["websiteEndpoint"].(string), "{bucket}") {
				WriteError(w, 400, "websiteEndpoint must name the bucket as {bucket}, such as https://{bucket}.{region}-static.digitaloceanspaces.com")
				return
			}
			probe := &deployment.Target{Kind: deployment.KindS3Bucket, Address: address}
			ok, words := h.providers.CheckS3Bucket(r.Context(), deployment.S3BucketConfig(probe))
			if !ok {
				WriteError(w, 422, "not attached — %s", words)
				return
			}
			detail = words
			t = &deployment.Target{
				Name: in.Name, Kind: deployment.KindS3Bucket, Provider: "", Origin: deployment.OriginAttached,
				Address:      address,
				Capabilities: deployment.TargetCapabilities{PersistentVolumes: false, Previews: false, OwnLiveness: true, Rollout: []string{"replace"}},
				Ingress:      map[string]any{"binding": "provider-issued"},
				NeedsMode:    "managed", SecretsDelivery: "none", Status: deployment.TargetReady,
			}
		default:
			WriteError(w, 400, "kind must be %s, %s, %s or %s", deployment.KindDockerHost, deployment.KindPortainer, deployment.KindDOApp, deployment.KindS3Bucket)
			return
		}
		if in.DryRun {
			WriteJSON(w, 200, TargetCheck{OK: true, Detail: detail, DryRun: true})
			return
		}
		if in.Domain != "" {
			t.Ingress["domain"] = in.Domain
		}
		if in.Evals {
			t.Ingress[deployment.IngressEvals] = true
		}
		saved, err := h.deployment.Targets.Put(r.Context(), t)
		if err != nil {
			WriteFault(w, err)
			return
		}
		payload := map[string]any{"kind": saved.Kind, "answered": detail}
		for _, k := range []string{"hostname", "user", "port", "keyFrom", "urlFrom", "apiKeyFrom", "environment", "endpoint", "region", "websiteEndpoint", "accessKeyFrom", "secretKeyFrom"} {
			if v, ok := saved.Address[k]; ok {
				payload[k] = v
			}
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "target.attached", saved.Name, payload)
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "target.checked", saved.Name, map[string]any{"ok": true, "detail": detail})
		WriteJSON(w, 200, TargetAttached{OK: true, Target: saved, Detail: detail})
	})
}

// The target attachment routes' responses. A dry run checks without
// saving; a DigitalOcean account also reports its container registry.
type (
	TargetCheck struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
		DryRun bool   `json:"dryRun"`
	}
	CloudAccountCheck struct {
		OK       bool   `json:"ok"`
		Detail   string `json:"detail"`
		DryRun   bool   `json:"dryRun"`
		Registry string `json:"registry"`
	}
	TargetAttached struct {
		OK     bool               `json:"ok"`
		Target *deployment.Target `json:"target"`
		Detail string             `json:"detail"`
	}
	CloudTargetAttached struct {
		OK       bool               `json:"ok"`
		Target   *deployment.Target `json:"target"`
		Detail   string             `json:"detail"`
		Registry string             `json:"registry"`
	}
	TargetEdited struct {
		OK     bool               `json:"ok"`
		Target *deployment.Target `json:"target"`
	}
)

// EditTarget: PATCH /targets/{name} {domain?, persistentVolumes?,
// previews?, needsMode?, evals?, rehearsals?, copyRunner?} — the facts AppTown cannot read off a target
// and the owner must state. Attached targets only; every change is an
// audit row carrying before and after.
func (h Targets) EditTarget(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		ExpectedRevision  *int64  `json:"expectedRevision"`
		Domain            *string `json:"domain"`
		PersistentVolumes *bool   `json:"persistentVolumes"`
		Previews          *bool   `json:"previews"`
		NeedsMode         *string `json:"needsMode"`
		Evals             *bool   `json:"evals"`
		Rehearsals        *bool   `json:"rehearsals"`
		CopyRunner        *string `json:"copyRunner"`
	}) {
		saved, err := h.deployment.Targets.EditSettings(r.Context(), r.PathValue("name"), deployment.TargetSettingsEdit{
			ExpectedRevision: in.ExpectedRevision, Domain: in.Domain, PersistentVolumes: in.PersistentVolumes, Previews: in.Previews, NeedsMode: in.NeedsMode, Evals: in.Evals, Rehearsals: in.Rehearsals, CopyRunner: in.CopyRunner,
		}, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, TargetEdited{OK: true, Target: saved})
	})
}

func (h Targets) bindCloudAccount(ctx context.Context, name string, cfg map[string]any) {
	if _, err := h.integrations.Bindings.Upsert(ctx, name, digitalocean.CapCloudAccount, "digitalocean", encodedJSON(cfg), false); err != nil {
		log.Printf("integration %s: recording its configuration: %v", name, err)
	}
}
