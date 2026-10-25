package httpapi

import (
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// Targets: GET /targets — the places the instance offers, the
// catalog of kinds, and the groups an offer can be made to.
func (h Targets) Targets(w http.ResponseWriter, r *http.Request) {
	views, err := h.targetViews(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	groups, err := h.access.OfferGroups(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, TargetList{Targets: views, Kinds: deployment.Kinds(), Groups: groups})
}

// OfferTarget: POST /targets/{name}/offers {group, maxStanding,
// audiences, data} — offer the target to a group ("*" = everyone). One
// offer per group per target; offering again replaces the caps.
func (h Targets) OfferTarget(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Group string `json:"group"`
		deployment.OfferCaps
	}) {
		t, err := h.deployment.OfferTarget(r.Context(), r.PathValue("name"), in.Group, in.OfferCaps)
		if err != nil {
			WriteFault(w, err)
			return
		}
		// The offer is saved; a failed read of the list must not answer
		// as if the target had none.
		offers, err := h.deployment.Targets.Offers(r.Context(), t.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, TargetOffers{OK: true, Offers: offers})
	})
}

// RevokeOffer: DELETE /targets/{name}/offers/{id} — withdraw an
// offer. Deployments already bound stay bound: withdrawing an offer
// stops new bindings, it does not tear anything down.
func (h Targets) RevokeOffer(w http.ResponseWriter, r *http.Request) {
	t, err := h.deployment.Targets.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		writeReadFault(w, err, "no target named %q", r.PathValue("name"))
		return
	}
	id, _ := PathID(r, "id")
	if _, err := h.deployment.Targets.RevokeOffer(r.Context(), t, id, Actor(r)); err != nil {
		if errors.Is(err, deployment.ErrOfferMissing) {
			WriteError(w, 404, "no offer %d on %s", id, t.Name)
			return
		}
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ProjectTargets: GET /projects/{slug}/targets — what this project
// may bind an environment to, each with the offer it comes through.
func (h Targets) ProjectTargets(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	ts, err := h.deployment.Targets.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []OfferedTarget{}
	for _, t := range ts {
		if !t.Status.Offerable() {
			continue
		}
		o, why := h.deployment.Targets.OfferFor(r.Context(), t, p.OwnerGroup)
		if o == nil {
			_ = why
			continue
		}
		n, err := h.deployment.Deployments.StandingCount(r.Context(), p.ID, t.ID, "")
		if err != nil {
			WriteFault(w, err)
			return
		}
		out = append(out, OfferedTarget{Name: t.Name, Kind: t.Kind, KindInfo: deployment.DescribeTarget(t), Address: t.Address, Capabilities: t.Capabilities,
			Ingress: t.Ingress, NeedsMode: t.NeedsMode, Origin: t.Origin, Offer: o, Bound: n})
	}
	WriteJSON(w, 200, ProjectTargets{Targets: out, Group: p.OwnerGroup})
}

// TargetList is every target, the kinds a target can be, and the groups a
// target can be offered to.
type TargetList struct {
	Targets []TargetView          `json:"targets"`
	Kinds   []deployment.KindInfo `json:"kinds"`
	Groups  []string              `json:"groups"`
}

// TargetOffers is a target's offers after one was made.
type TargetOffers struct {
	OK     bool               `json:"ok"`
	Offers []deployment.Offer `json:"offers"`
}

// ProjectTargets is what a project may bind a deployment to, and the group
// its offers are judged for.
type ProjectTargets struct {
	Targets []OfferedTarget `json:"targets"`
	Group   string          `json:"group"`
}

// OfferedTarget is a target offered to a project, with the offer it comes
// through and how many of the project's deployments stand on it.
type OfferedTarget struct {
	Name         string                        `json:"name"`
	Kind         string                        `json:"kind"`
	KindInfo     deployment.KindInfo           `json:"kindInfo"`
	Address      map[string]any                `json:"address"`
	Capabilities deployment.TargetCapabilities `json:"capabilities"`
	Ingress      map[string]any                `json:"ingress"`
	NeedsMode    string                        `json:"needsMode"`
	Origin       string                        `json:"origin"`
	Offer        *deployment.Offer             `json:"offer"`
	Bound        int                           `json:"bound"`
}
