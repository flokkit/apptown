package deployment

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

// Offers (design/subsystems/deployment.md §15.7): the instance attaches, groups
// are offered, projects opt in. A project may bind to a target only through
// an offer to its owner group or to everyone; the built-in host and the
// targets the instance names as offered by construction need no offer row.

var (
	audiences   = []string{"team", "organisation", "partners", "public"}
	dataClasses = []string{"synthetic", "non-production", "production"}
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ValidateCaps refuses an audience or data class the vocabulary does not have.
func ValidateCaps(caps OfferCaps) error {
	for _, x := range caps.Audiences {
		if !contains(audiences, x) {
			return fmt.Errorf("audience %q is not one of %s", x, strings.Join(audiences, ", "))
		}
	}
	for _, x := range caps.Data {
		if !contains(dataClasses, x) {
			return fmt.Errorf("data %q is not one of %s", x, strings.Join(dataClasses, ", "))
		}
	}
	return nil
}

// OfferedByConstruction: the built-in host and the targets named at
// construction are a fresh AppTown's behaviour and carry no offer row.
func (s *Targets) OfferedByConstruction(t *Target) bool {
	return t.Origin == OriginBuiltIn || contains(s.implicit, t.Name)
}

// OfferFor is the offer a project binds to a target through, given the
// project's owner group, or the sentence saying there is none. An offer to
// the group beats one to everyone.
func (s *Targets) OfferFor(ctx context.Context, t *Target, ownerGroup string) (*Offer, string) {
	offer, err := s.offerFor(ctx, deploymentsql.New(s.db.SQL()), t, ownerGroup)
	if err != nil {
		return nil, err.Error()
	}
	return offer, ""
}

func (s *Targets) offerFor(ctx context.Context, q *deploymentsql.Queries, t *Target, ownerGroup string) (*Offer, error) {
	if s.OfferedByConstruction(t) {
		return &Offer{TargetID: t.ID, Group: "*", Implicit: true}, nil
	}
	offers, err := readOffers(ctx, q, t.ID)
	if err != nil {
		return nil, err
	}
	return selectOffer(t, ownerGroup, offers)
}
func selectOffer(t *Target, ownerGroup string, offers []Offer) (*Offer, error) {
	var everyone *Offer
	for i := range offers {
		switch offers[i].Group {
		case ownerGroup:
			if ownerGroup != "" {
				return &offers[i], nil
			}
		case "*":
			everyone = &offers[i]
		}
	}
	if everyone != nil {
		return everyone, nil
	}
	who := ownerGroup
	if who == "" {
		who = "this project's group"
	}
	return nil, &OfferDeniedError{fmt.Sprintf("%s is not offered to %s — an administrator offers targets to groups under Integrations → Deployment targets", t.Name, who)}
}

// Permits is the sentence for a deployment the offer's caps refuse, or "".
// standing is how many standing deployments the project already has on the
// target, not counting the one being bound.
func (o *Offer) Permits(t *Target, projectSlug string, attrs Attributes, standing int) string {
	if o.Implicit {
		return ""
	}
	if len(o.Caps.Audiences) > 0 && !contains(o.Caps.Audiences, attrs.Audience) {
		return fmt.Sprintf("%s is offered for audiences %s, not %s", t.Name, strings.Join(o.Caps.Audiences, "/"), attrs.Audience)
	}
	if len(o.Caps.Data) > 0 && !contains(o.Caps.Data, attrs.DataClass) {
		return fmt.Sprintf("%s is offered for %s data, not %s", t.Name, strings.Join(o.Caps.Data, "/"), attrs.DataClass)
	}
	if o.Caps.MaxStanding > 0 && standing >= o.Caps.MaxStanding {
		return fmt.Sprintf("%s is offered for %d standing deployment(s) per project; %s already has %d", t.Name, o.Caps.MaxStanding, projectSlug, standing)
	}
	return ""
}

// StandingCount is how many standing deployments a project has on a target,
// leaving out one by name (the one being bound or rebound).
func (s *Deployments) StandingCount(ctx context.Context, projectID, targetID ids.ID, excluding string) (int, error) {
	n, err := deploymentsql.New(s.db.SQL()).StandingCount(ctx, deploymentsql.StandingCountParams{ProjectID: projectID, TargetID: targetID, Kind: Standing.String(), Excluding: excluding})
	return int(n), err
}
