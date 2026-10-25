package deployment

import (
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"testing"
)

func TestCheckedTargetAdmissionRefusesBeforeReadingOrProbing(t *testing.T) {
	denied := errors.New("authority denied")
	for _, kind := range []string{KindDockerHost, KindPortainer, KindDOApp, KindS3Bucket} {
		for _, dry := range []bool{false, true} {
			authority := &requestAuthority{deny: denied}
			// No record or provider collaborators: denial must precede their use.
			service := &Service{authority: authority}
			if _, err := service.AttachTarget(t.Context(), TargetAttachment{Kind: kind, Name: "host", DryRun: dry}); !errors.Is(err, denied) {
				t.Fatalf("%s dry=%v: %v", kind, dry, err)
			}
			if len(authority.seen) != 1 || authority.seen[0].Capability != access.CapPlatformTargets || authority.seen[0].Context.Consequential == dry {
				t.Fatalf("attachment permission: %+v", authority.seen)
			}
		}
	}
	authority := &requestAuthority{deny: denied}
	service := &Service{authority: authority}
	if _, err := service.OfferTarget(t.Context(), "host", "*", OfferCaps{}); !errors.Is(err, denied) {
		t.Fatalf("offer authority: %v", err)
	}
	if len(authority.seen) != 1 || !authority.seen[0].Context.Consequential {
		t.Fatalf("offer permission: %+v", authority.seen)
	}
}

func TestDirectPortainerAttachmentUsesTheIngressDomainContract(t *testing.T) {
	for _, domain := range []string{"user@apps.example", "apps.example?query", "apps.example#fragment", "apps.example:8443"} {
		service := &Service{authority: &requestAuthority{}}
		// A domain refusal must precede missing credentials or provider access.
		if _, err := service.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "host", Domain: domain}); !errors.Is(err, ErrTargetDomain) {
			t.Fatalf("domain %q: %v", domain, err)
		}
	}
}
