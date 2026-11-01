package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// TargetPage: GET /targets/{name} — one target's view plus its
// history (the audit rows under its name, newest first).
func (h Targets) TargetPage(w http.ResponseWriter, r *http.Request) {
	t, err := h.deployment.Targets.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "no target named %q", r.PathValue("name"))
		return
	}
	views, err := h.targetViews(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	var view *TargetView
	for i := range views {
		if views[i].Target.ID == t.ID {
			view = &views[i]
		}
	}
	history := h.targetHistory(r.Context(), t.Name, 50)
	WriteJSON(w, 200, TargetPage{Target: view, History: history, BuiltIn: t.Origin == deployment.OriginBuiltIn, Registry: lastRegistryReading(history)})
}

// TargetCapacity: GET /targets/{name}/capacity — how full the target is:
// a fixed host's total, promised limits, use and disk; an elastic one's
// sizes and estimated cost. Its own route, so a slow host never holds the
// target's page.
func (h Targets) TargetCapacity(w http.ResponseWriter, r *http.Request) {
	if _, err := h.deployment.Targets.ByName(r.Context(), r.PathValue("name")); err != nil {
		WriteError(w, 404, "no target named %q", r.PathValue("name"))
		return
	}
	c, err := h.deployment.Capacity(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, c)
}

// lastRegistryReading is the registry reading the newest check recorded,
// with when it was read; nil when no check has read one.
func lastRegistryReading(history []TargetEvent) *deployment.RegistryReading {
	for _, e := range history {
		if e.Kind != "target.checked" {
			continue
		}
		var p struct {
			Registry *deployment.RegistryReading `json:"registry"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.Registry != nil {
			p.Registry.ReadAt = e.At
			return p.Registry
		}
	}
	return nil
}

// CheckTarget: POST /targets/{name}/check — ask the target whether it
// answers, by kind, and record the answer as its status.
func (h Targets) CheckTarget(w http.ResponseWriter, r *http.Request) {
	t, err := h.deployment.Targets.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "no target named %q", r.PathValue("name"))
		return
	}
	if err := t.Status.Available(); err != nil {
		WriteError(w, 409, "%s is retired", t.Name)
		return
	}
	c, err := h.providers.ObserveTarget(r.Context(), t, actor(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	status := deployment.TargetUnreachable
	if c.OK {
		status = deployment.TargetReady
	}

	WriteJSON(w, 200, TargetChecked{OK: c.OK, Detail: c.Detail, Status: status})
}

// RetireTarget: POST /targets/{name}/retire {"confirm":"<name>"} — take a
// target out of service. The body must repeat the target's name as confirm;
// an empty body is refused with the same sentence, naming the body to send.
// Refused for the built-in host, and while any standing deployment is bound
// to it (they are named: move or retire them first). Offers are withdrawn
// with it.
func (h Targets) RetireTarget(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Confirm string `json:"confirm"`
	}) {
		if strings.TrimSpace(in.Confirm) != name {
			confirm, _ := json.Marshal(map[string]string{"confirm": name})
			WriteError(w, 400, "confirm must equal the target name: send %s", confirm)
			return
		}
		if err := h.deployment.Targets.Retire(r.Context(), name, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, TargetRetired{OK: true, Status: deployment.TargetRetired})
	})
}

func (h Targets) targetViews(ctx context.Context) ([]TargetView, error) {
	ts, err := h.deployment.Targets.All(ctx)
	if err != nil {
		return nil, err
	}
	out := []TargetView{}
	for _, t := range ts {
		check, err := h.deployment.Targets.RecordedCheck(ctx, t)
		if err != nil {
			return nil, err
		}
		v := TargetView{Check: check, Target: *t, StatusLabel: t.Status.Display(), KindInfo: deployment.DescribeTarget(t), Offers: []deployment.Offer{}, Deployments: []TargetDeployment{}, Copies: dataprotection.CopiesOn(t)}
		if check.Unknown && t.Status != deployment.TargetRetired {
			v.StatusLabel = deployment.UnknownTargetLabel()
		}
		if h.deployment.Targets.OfferedByConstruction(t) {
			v.Offers = append(v.Offers, deployment.Offer{TargetID: t.ID, Group: "*", Implicit: true})
		} else {
			offers, err := h.deployment.Targets.Offers(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			v.Offers = append(v.Offers, offers...)
		}
		standing, err := h.deployment.Deployments.OnTarget(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range standing {
			if !e.Standing() {
				continue
			}
			v.Deployments = append(v.Deployments, TargetDeployment{Project: e.Project, Name: e.Name, URL: e.URL, Generation: e.ObservedGeneration,
				Readiness: e.Readiness.String(), StatusLabel: e.Readiness.Display(), Audience: e.Audience, Data: e.DataClass})
		}
		if v.Retained, err = h.deployment.Retained.CountOpen(ctx, t.ID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// targetHistory is the audit trail under a target's name: offers made and
// withdrawn, checks, edits, retirement.
func (h Targets) targetHistory(ctx context.Context, name string, limit int) []TargetEvent {
	out := []TargetEvent{}
	lines, err := audit.Recent(ctx, h.db, audit.Filter{KindPrefix: "target.", Subject: name, Limit: limit})
	if err != nil {
		return out
	}
	for _, l := range lines {
		payload := l.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		out = append(out, TargetEvent{At: l.At, Actor: l.Actor, Kind: l.Kind, Payload: payload})
	}
	return out
}

// TargetPage is one target with its offers, deployments and history.
type TargetPage struct {
	Target  *TargetView   `json:"target"`
	History []TargetEvent `json:"history"`
	BuiltIn bool          `json:"builtIn"`
	// Registry is an App Platform target's container registry as its
	// newest check read it, with when.
	Registry *deployment.RegistryReading `json:"registry,omitempty"`
}

// TargetChecked is a target's answer to a check and the status recorded.
type TargetChecked struct {
	OK     bool                   `json:"ok"`
	Detail string                 `json:"detail"`
	Status deployment.TargetState `json:"status"`
}

// TargetRetired is a target taken out of service.
type TargetRetired struct {
	OK     bool                   `json:"ok"`
	Status deployment.TargetState `json:"status"`
}

// TargetDeployment is a standing deployment on a target.
type TargetDeployment struct {
	StatusLabel deployment.ProgressLabel `json:"statusLabel"`
	Project     string                   `json:"project"`
	Name        string                   `json:"name"`
	URL         string                   `json:"url"`
	Generation  int64                    `json:"generation"`
	Readiness   string                   `json:"readiness"`
	Audience    string                   `json:"audience"`
	Data        string                   `json:"data"`
}

type TargetView struct {
	Check       deployment.TargetReading `json:"check"`
	StatusLabel deployment.TargetLabel   `json:"statusLabel"`
	deployment.Target
	KindInfo    deployment.KindInfo `json:"kindInfo"`
	Offers      []deployment.Offer  `json:"offers"`
	Deployments []TargetDeployment  `json:"deployments"`
	Retained    int                 `json:"retained"`
	// Copies says whether copies of its deployments' data can run here,
	// and why not (no copy runner named); absent where AppTown takes none.
	Copies *dataprotection.Availability `json:"copies,omitempty"`
}

type TargetEvent struct {
	At      string          `json:"at"`
	Actor   string          `json:"actor"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
