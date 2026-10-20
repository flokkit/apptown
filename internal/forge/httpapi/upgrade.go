package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/projectlifecycle"
)

// Upgrade is the AppTown rename's one-time upgrade (apptown upgrade).
type Upgrade struct {
	lifecycle *projectlifecycle.Service
	work      projectlifecycle.UpgradeWork
	access    *identity.Service
}

func NewUpgrade(lifecycle *projectlifecycle.Service, work projectlifecycle.UpgradeWork, access *identity.Service) Upgrade {
	return Upgrade{lifecycle: lifecycle, work: work, access: access}
}

// UpgradeView is the upgrade's report, project by project.
type UpgradeView struct {
	Projects []projectlifecycle.UpgradedProject `json:"projects"`
	Library  []string                           `json:"library"`
}

// UpgradeToAppTown: POST /api/v1/upgrade/apptown — rename .forge to
// .apptown in every project and advance adoptions to the renamed content.
func (h Upgrade) UpgradeToAppTown(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapPlatformOperate, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	report, err := h.lifecycle.UpgradeToAppTown(r.Context(), Actor(r), h.work)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := UpgradeView{Projects: report.Projects, Library: report.Library}
	if out.Projects == nil {
		out.Projects = []projectlifecycle.UpgradedProject{}
	}
	if out.Library == nil {
		out.Library = []string{}
	}
	WriteJSON(w, 200, out)
}
