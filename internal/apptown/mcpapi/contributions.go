package mcpapi

import (
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ContributionsDependencies struct {
	Scope         scope
	Work          *workflow.Service
	Contributions *repositoryedit.Contributions
	Adoption      *adoption.Service
}
type contributions struct {
	scope         scope
	work          *workflow.Service
	contributions *repositoryedit.Contributions
	adoption      *adoption.Service
}

func NewContributions(deps ContributionsDependencies) []toolEntry {
	h := contributions{scope: deps.Scope, work: deps.Work, contributions: deps.Contributions, adoption: deps.Adoption}
	return []toolEntry{{Name: "checkout_change",
		order: 17, Description: "Returns what a local Git checkout needs to work on a Change: the repository URL, the Change's ref and current tip, and a short-lived credential that can fetch main and this Change and push only this Change's ref. " + "Use the returned git commands as arguments, not as a shell string.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"), command: ownerCommand("contributions.checkoutChange", h.checkoutChange)}, {Name: "renew_change_grant",
		order:       18,
		Description: "Mints a fresh push credential for a Change whose earlier one expired or was consumed by a push. Same bounds as checkout_change.",
		InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"),
		command:     ownerCommand("contributions.renewChangeGrant", h.renewChangeGrant),
	},
		{
			Name:  "sync_change",
			order: 19,
			Description: "Tells AppTown you pushed a revision to a Change's ref: the Change's workspace adopts that tip, earlier check results no longer answer for it, and the valid next actions are returned. " +
				"Pass the SHA you pushed and the tip you expected before you pushed; a mismatch is reported with facts, never resolved silently.",
			InputSchema: schemaObject(map[string]any{
				"id":                    schemaID("The Change's id."),
				"pushed_sha":            schemaString("The commit you pushed (the new tip of the Change ref)."),
				"expected_previous_tip": schemaString("The tip checkout_change reported (empty for a new ref)."),
			}, "id", "pushed_sha"),
			command: ownerCommand("contributions.syncChange", h.syncChange),
		},
	}
}
func (h contributions) checkoutChange(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	return h.mintCheckout(c, ch, p)
}
func (h contributions) renewChangeGrant(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	return h.mintCheckout(c, ch, p)
}
func (h contributions) syncChange(c *Call, args map[string]any) (any, error) {
	ch, _, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	synced, err := h.contributions.Sync(c.Context, ch.ID, argString(args, "pushed_sha"), argString(args, "expected_previous_tip"))
	if err != nil {
		return nil, contributionError(err)
	}
	hubTip, wsTip := synced.Tip, synced.PreviousTip

	return map[string]any{
		"changeId": ch.ID, "tip": hubTip, "previousTip": wsTip, "webUrl": c.changeURL(ch.ID),
		"invalidated": "check results and previews from before this revision no longer answer for it",
		"nextActions": []string{"run_change_checks", "preview_change", "request_change_landing", "checkout_change (for another push)"},
	}, nil
}

func (h contributions) mintCheckout(c *Call, ch *workflow.Change, p *project.Project) (map[string]any, error) {
	grant, err := h.contributions.Checkout(c.Context, ch.ID)
	if err != nil {
		return nil, contributionError(err)
	}
	tok, t, tip, ref, pushRef := grant.Token, grant.Ticket, grant.Tip, grant.Ref, grant.PushRef

	db := p.DefaultBranch
	out := map[string]any{
		"changeId": ch.ID, "webUrl": c.changeURL(ch.ID),
		"repositoryUrl": c.gitURL(p), "defaultBranch": db, "ref": ref, "branch": ch.Branch,
		"baseSha": ch.BaseSHA, "expectedTip": tip, "ticketId": t.ID, "expiresAt": t.ExpiresAt,
		// The credential travels in an environment variable and an inline
		// credential helper, never in the remote URL: a URL credential lands
		// in .git/config and shell history, and the client then has to scrub
		// it (Claude Code did, on its first live use). Same bounds either way.
		"credential": map[string]any{"username": "x-apptown-ticket", "password": tok, "env": "APPTOWN_GIT_TICKET",
			"note": "one Change, one ref, minutes-long, consumed by the first successful push. The git commands below read it from the APPTOWN_GIT_TICKET environment variable through a one-line credential helper, so it never enters the remote URL or .git/config"},
		"git": map[string]any{
			"note":   "Each command must see APPTOWN_GIT_TICKET in its own environment. A tool that runs every command in a fresh shell must use the `shell` form, which sets it inline; an `export` in an earlier call does not carry over (that is the 'Authentication failed' you get otherwise).",
			"clone":  gitWithTicket("clone", "--branch", db, c.gitURL(p), "<dir>"),
			"fetch":  gitWithTicket("fetch", "origin", "+"+ref+":"+ref),
			"switch": []string{"git", "switch", ch.Branch},
			"push":   gitWithTicket("push", "origin", ch.Branch+":"+pushRef),
			"shell": map[string]string{
				"clone": shellWithTicket(tok, "clone", "--branch", db, c.gitURL(p), "<dir>"),
				"fetch": shellWithTicket(tok, "fetch", "origin", "+"+ref+":"+ref),
				"push":  shellWithTicket(tok, "push", "origin", ch.Branch+":"+pushRef),
			},
		},
		"next": "commit locally, push with the credential using the push command (it lands in a holding ref; AppTown then moves the Change's branch if this credential still holds it), then call sync_change with the pushed SHA and this expectedTip",
	}
	// The ways of working are offered at the moment work starts, sized, and
	// left to the agent to fetch (get_project_guidance) — never injected.
	if offer := guidanceOffer(c.Context, h.adoption, p.ID); offer["guidanceNote"] != nil {
		out["guidance"] = map[string]any{"blocks": offer["guidance"], "bytes": offer["guidanceBytes"], "note": offer["guidanceNote"]}
	}
	if tip == "" {
		out["git"].(map[string]any)["switch"] = []string{"git", "switch", "-c", ch.Branch, db}
	}
	return out, nil
}
