package fly

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Account is what a token answers about itself and where Fly runs: the
// orgs it can manage, and the regions Machines can be placed in.
type Account struct {
	// Orgs are the org slugs the token reaches as a whole (an org token
	// reaches one; a person's token several). Apps is non-empty for a token
	// limited to named apps (a deploy token), which cannot create apps.
	Orgs []string
	Apps []string
	// Regions are Fly's region codes and whether a paid plan is needed.
	Regions []Region
}

// Region is one of Fly's regions.
type Region struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	PaidPlanOnly bool   `json:"paidPlanOnly"`
}

// ReadAccount asks Fly what the token reaches (GET /v1/tokens/current) and
// which regions there are (GET /v1/platform/regions). It changes nothing.
func ReadAccount(ctx context.Context, c *Client) (Account, error) {
	var tok struct {
		Tokens []struct {
			OrgSlug string   `json:"org_slug"`
			Apps    []string `json:"apps"`
		} `json:"tokens"`
	}
	if err := c.do(ctx, "GET", "/v1/tokens/current", nil, &tok); err != nil {
		return Account{}, fmt.Errorf("asking Fly what the token reaches: %w", err)
	}
	var out Account
	for _, t := range tok.Tokens {
		if len(t.Apps) > 0 {
			out.Apps = append(out.Apps, t.Apps...)
			continue
		}
		if t.OrgSlug != "" && !slices.Contains(out.Orgs, t.OrgSlug) {
			out.Orgs = append(out.Orgs, t.OrgSlug)
		}
	}
	sort.Strings(out.Orgs)
	var regions struct {
		Regions []struct {
			Code         string `json:"code"`
			Name         string `json:"name"`
			Deprecated   bool   `json:"deprecated"`
			PaidPlanOnly bool   `json:"requires_paid_plan"`
		} `json:"regions"`
	}
	if err := c.do(ctx, "GET", "/v1/platform/regions", nil, &regions); err != nil {
		return Account{}, fmt.Errorf("asking Fly for its regions: %w", err)
	}
	for _, r := range regions.Regions {
		if r.Deprecated {
			continue
		}
		out.Regions = append(out.Regions, Region{Code: r.Code, Name: r.Name, PaidPlanOnly: r.PaidPlanOnly})
	}
	sort.Slice(out.Regions, func(i, j int) bool { return out.Regions[i].Code < out.Regions[j].Code })
	return out, nil
}

// ChooseOrg says which org a target uses, from what the token reaches and
// what the administrator named (""), or why none can be used. A token that
// reaches one org uses it; one that reaches several needs it named; a token
// limited to apps cannot create an app at all.
func (a Account) ChooseOrg(named string) (string, error) {
	named = strings.TrimSpace(named)
	switch {
	case len(a.Orgs) == 0 && len(a.Apps) > 0:
		return "", fmt.Errorf("this token can reach only the app %s; a Fly target needs an org token (fly tokens create org), which can create apps", strings.Join(a.Apps, ", "))
	case len(a.Orgs) == 0:
		return "", fmt.Errorf("this token reaches no Fly org; a Fly target needs an org token (fly tokens create org)")
	case named != "" && !slices.Contains(a.Orgs, named):
		return "", fmt.Errorf("this token does not reach the org %s; it reaches %s", named, strings.Join(a.Orgs, ", "))
	case named != "":
		return named, nil
	case len(a.Orgs) == 1:
		return a.Orgs[0], nil
	}
	return "", fmt.Errorf("this token reaches several orgs (%s); name the one this target uses", strings.Join(a.Orgs, ", "))
}

// Region is the named region, or why it cannot be used.
func (a Account) Region(code string) (Region, error) {
	for _, r := range a.Regions {
		if r.Code == code {
			return r, nil
		}
	}
	return Region{}, fmt.Errorf("Fly has no region %q", code)
}
