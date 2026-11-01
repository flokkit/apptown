package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/deployment"
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
	jsonCommand(w, r, jsonBody{}, func(in deployment.TargetAttachment) {
		result, err := h.deployment.AttachTarget(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if in.Kind == deployment.KindDOApp {
			if in.DryRun {
				WriteJSON(w, 200, CloudAccountCheck{OK: true, Detail: result.Detail, DryRun: true, Registry: result.Registry})
				return
			}
			WriteJSON(w, 200, CloudTargetAttached{OK: true, Target: result.Target, Detail: result.Detail, Registry: result.Registry})
			return
		}
		if in.DryRun {
			WriteJSON(w, 200, TargetCheck{OK: true, Detail: result.Detail, DryRun: true})
			return
		}
		WriteJSON(w, 200, TargetAttached{OK: true, Target: result.Target, Detail: result.Detail})
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
		Environment       *string `json:"environment"`
	}) {
		saved, err := h.deployment.Targets.EditSettings(r.Context(), r.PathValue("name"), deployment.TargetSettingsEdit{
			ExpectedRevision: in.ExpectedRevision, Domain: in.Domain, PersistentVolumes: in.PersistentVolumes, Previews: in.Previews, NeedsMode: in.NeedsMode, Evals: in.Evals, Rehearsals: in.Rehearsals, CopyRunner: in.CopyRunner, Environment: in.Environment,
		}, actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, TargetEdited{OK: true, Target: saved})
	})
}
