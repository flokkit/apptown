package deployment

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// A deployment's classification is its audience and data class (§2.3a).
// Every rule that reads them reads the current record at its own act, so a
// change applies to AppTown's rules at once and to the app's data handling
// from its next release. No target applies an audience: nothing is sent to
// the target when it changes.
//
// Raising a guard needs the configure capability. Lowering one also needs
// the release grant when the deployment was protected, a verified person,
// and the stated meaning that person accepted; it is recorded as its own act.

// audienceRanks and dataRanks order the values from least to most guarded.
var (
	audienceRanks = []string{"team", "organisation", "partners", "public"}
	dataRanks     = []string{"synthetic", "non-production", "production"}
)

// lowers says whether going from was to to lowers either guard.
func lowers(was, to Attributes) bool {
	return slices.Index(audienceRanks, to.Audience) < slices.Index(audienceRanks, was.Audience) ||
		slices.Index(dataRanks, to.DataClass) < slices.Index(dataRanks, was.DataClass)
}

func protectedAttributes(at Attributes) bool {
	return at.Audience == "partners" || at.Audience == "public" || at.DataClass == "production"
}

var audienceWords = map[string]string{
	"team":         "only the project's team",
	"organisation": "the organisation",
	"partners":     "partners outside the organisation",
	"public":       "the public",
}

var dataWords = map[string]string{
	"synthetic":      "test data that AppTown may discard on a release",
	"non-production": "data that is not production data",
	"production":     "production data",
}

// classificationMeaning is what a person accepts when lowering a guard.
func classificationMeaning(label string, to Attributes) string {
	return fmt.Sprintf("I state that %s serves %s and holds %s", label, audienceWords[to.Audience], dataWords[to.DataClass])
}

// classificationGuard is one rule a classification turns on, in the words
// the settings page shows. The list is the one place those words live.
type classificationGuard struct {
	on   func(Attributes) bool
	text string
}

// classificationGuards are the rules a classification turns on at t. Each
// says only what that target does: AppTown's own daily copies and weekly
// checks run on the managed-data kinds, and a release discards a changed
// service's synthetic data only on a Docker host.
func classificationGuards(t *Target) []classificationGuard {
	production := func(at Attributes) bool { return at.DataClass == "production" }
	guards := []classificationGuard{
		{protectedAttributes, "Only people with release permission can deploy here; queued and automatic deploys are checked again"},
		{production, "AppTown keeps the newest 14 backup sets instead of 7, and Attention warns when backups are late or missing"},
	}
	kind := describeKind(t.Kind)
	if kind.dailyCopies {
		guards = append(guards, classificationGuard{production, "For data AppTown can copy, it takes a copy each day and checks each week that a copy comes back, once backups are set up: a backup store and, where the target needs one, a copy runner"})
	}
	if kind.discardsSynthetic {
		guards = append(guards, classificationGuard{func(at Attributes) bool { return at.DataClass != "synthetic" }, "A release keeps a service's data when the service's settings change, instead of discarding it"})
	}
	return guards
}

// ClassificationChoice is one value a select offers, with why it is refused.
type ClassificationChoice struct {
	Value   string `json:"value"`
	Refusal string `json:"refusal,omitempty"`
}

// ClassificationChange is what saving a proposed classification would do.
type ClassificationChange struct {
	Audience string   `json:"audience"`
	Data     string   `json:"data"`
	Lowers   bool     `json:"lowers"`
	Adds     []string `json:"adds"`
	Removes  []string `json:"removes"`
	// Meaning is the sentence a person accepts to lower a guard; "" when
	// the change lowers none.
	Meaning string `json:"meaning,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

// ClassificationView is a deployment's classification with the values its
// target and offer allow, and, when one is proposed, what it would change.
type ClassificationView struct {
	Audience  string                 `json:"audience"`
	Data      string                 `json:"data"`
	Audiences []ClassificationChoice `json:"audiences"`
	DataClass []ClassificationChoice `json:"dataClasses"`
	// Effect says when a change applies; the same for every target.
	Effect   string                `json:"effect"`
	Proposed *ClassificationChange `json:"proposed,omitempty"`
}

// ClassificationSettings is one compare-and-set edit: Was is what the person
// edited from, and Meaning the sentence they accepted when lowering.
type ClassificationSettings struct {
	Audience string `json:"audience"`
	Data     string `json:"data"`
	Was      struct {
		Audience string `json:"audience"`
		Data     string `json:"data"`
	} `json:"was"`
	Meaning string `json:"meaning,omitempty"`
}

var (
	errClassificationMoved = fault.New(fault.Conflict, "the audience or data changed while you were editing; review the current values and save again")
	errClassificationKind  = fault.New(fault.Invalid, "only a standing deployment has an audience and data to change")
	errLoweringByAgent     = fault.New(fault.Forbidden, "lowering a deployment's audience or data needs a person signed in, not an agent or a client")
)

func classificationEffect(t *Target) string {
	return fmt.Sprintf("Who may deploy changes at once; backups follow from the next backup or check, and the app's data from its next release. A deploy already waiting is refused; ask for it again. Nothing is changed on %s: the audience is AppTown's rule, not a sign-in in front of the app.", t.Name)
}

// classificationRefusal is why the target, its offer or the deployment's
// starting copy refuses the change to to, or "".
func (s *Service) classificationRefusal(ctx context.Context, q *deploymentsql.Queries, dep *Deployment, t *Target, ownerGroup string, to Attributes) (string, error) {
	if why := t.refusesAudienceValue(to.Audience); why != "" {
		return why + "; choose public", nil
	}
	offer, err := s.Targets.offerFor(ctx, q, t, ownerGroup)
	var denied *offerDeniedError
	if errors.As(err, &denied) {
		return denied.Reason, nil
	}
	if err != nil {
		return "", err
	}
	// The deployment is already counted where it stands, so the count is
	// not asked again.
	if why := offer.permits(t, "", Attributes{Audience: to.Audience, DataClass: to.DataClass}, 0); why != "" {
		return why, nil
	}
	if dep.DataClass == "production" && to.DataClass != "production" && !dep.StartFrom.IsZero() {
		if s.starter == nil {
			return "", errNoCopyStarter
		}
		class, err := s.starter.StartClass(ctx, dep.StartFrom)
		if err != nil {
			return "", err
		}
		if class == "production" {
			return "it started from a copy of production data, which stays in a deployment that holds production data; to hold other data, make a new deployment", nil
		}
	}
	return "", nil
}

func (s *Service) classificationContext(ctx context.Context, projectID ids.ID, name string) (*Deployment, *Target, string, error) {
	if s.configuration.Projects == nil {
		return nil, nil, "", errors.New("deployment configuration is not configured")
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, nil, "", err
	}
	if !dep.Standing() {
		return nil, nil, "", errClassificationKind
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, nil, "", err
	}
	proj, err := s.configuration.Projects.Project(ctx, projectID)
	if err != nil {
		return nil, nil, "", err
	}
	return dep, t, proj.OwnerGroup, nil
}

// Classification reads a deployment's classification with its choices, and
// what saving to would do when to is given. The values refused are
// refused by SetClassification too: both ask classificationRefusal.
func (s *Service) Classification(ctx context.Context, projectID ids.ID, name string, to *Attributes) (ClassificationView, error) {
	dep, t, group, err := s.classificationContext(ctx, projectID, name)
	if err != nil {
		return ClassificationView{}, err
	}
	q := deploymentsql.New(s.db.SQL())
	was := Attributes{Audience: dep.Audience, DataClass: dep.DataClass}
	out := ClassificationView{Audience: dep.Audience, Data: dep.DataClass, Effect: classificationEffect(t)}
	for _, a := range audienceRanks {
		why := ""
		if a != dep.Audience {
			if why, err = s.classificationRefusal(ctx, q, dep, t, group, Attributes{Audience: a, DataClass: dep.DataClass}); err != nil {
				return ClassificationView{}, err
			}
		}
		out.Audiences = append(out.Audiences, ClassificationChoice{Value: a, Refusal: why})
	}
	for _, d := range dataRanks {
		why := ""
		if d != dep.DataClass {
			if why, err = s.classificationRefusal(ctx, q, dep, t, group, Attributes{Audience: dep.Audience, DataClass: d}); err != nil {
				return ClassificationView{}, err
			}
		}
		out.DataClass = append(out.DataClass, ClassificationChoice{Value: d, Refusal: why})
	}
	if to == nil {
		return out, nil
	}
	proposed := Attributes{Feed: dep.Feed, Audience: to.Audience, DataClass: to.DataClass}
	was.Feed = dep.Feed
	if err := proposed.Validate(); err != nil {
		return ClassificationView{}, &configurationInputError{err}
	}
	change := &ClassificationChange{Audience: to.Audience, Data: to.DataClass, Lowers: lowers(was, proposed), Adds: []string{}, Removes: []string{}}
	for _, g := range classificationGuards(t) {
		switch before, after := g.on(was), g.on(proposed); {
		case after && !before:
			change.Adds = append(change.Adds, g.text)
		case before && !after:
			change.Removes = append(change.Removes, g.text)
		}
	}
	if change.Lowers {
		change.Meaning = classificationMeaning(dep.DisplayLabel, proposed)
	}
	if proposed != was {
		if change.Refusal, err = s.classificationRefusal(ctx, q, dep, t, group, proposed); err != nil {
			return ClassificationView{}, err
		}
	}
	out.Proposed = change
	return out, nil
}

// SetClassification changes a standing deployment's audience and data class
// (see the package comment above). It is refused, without effect, when the
// record moved from in.Was, when the target, its offer or the deployment's
// starting copy forbids the change, or when lowering lacks its authority or
// meaning.
func (s *Service) SetClassification(ctx context.Context, projectID ids.ID, name string, in ClassificationSettings) (*Deployment, error) {
	if s.configuration.Authority == nil || s.release.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	was := Attributes{Feed: "manual", Audience: in.Was.Audience, DataClass: in.Was.Data}
	to := Attributes{Feed: "manual", Audience: in.Audience, DataClass: in.Data}
	if err := to.Validate(); err != nil {
		return nil, &configurationInputError{err}
	}
	if err := was.Validate(); err != nil {
		return nil, &configurationInputError{fmt.Errorf("was: %w", err)}
	}
	dep, t, group, err := s.classificationContext(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	// Authority is decided on the values the person edited from; the
	// transaction refuses the change unless the record still holds them.
	resource := access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: resource, Context: access.Context{Route: "deployment.classify", Consequential: true}})
	if err != nil {
		return nil, err
	}
	if protectedAttributes(was) {
		if _, err := s.release.Authority.Require(ctx, access.Permission{Capability: access.CapReleasePromote, Resource: access.Resource{Type: "release-target", ID: name, ProjectID: projectID}, Context: access.Context{Route: "deployment.classify", Consequential: true}}); err != nil {
			return nil, err
		}
	}
	lowering := lowers(was, to)
	if lowering {
		if !actor.Verified {
			return nil, errLoweringByAgent
		}
		if want := classificationMeaning(dep.DisplayLabel, to); in.Meaning != want {
			return nil, fault.New(fault.Unprocessable, fmt.Sprintf("this lowers a guard on %s; confirm: %s", dep.DisplayLabel, want))
		}
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		// Configure's lock order: project, target, then deployment. The
		// target's row lock keeps an offer edit out until this commits.
		q := deploymentsql.New(tx.SQL())
		if _, err := q.TargetByNameForUpdate(ctx, t.Name); err != nil {
			return err
		}
		current, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if current.Audience != was.Audience || current.DataClass != was.DataClass {
			return fmt.Errorf("%w (now %s audience, %s data)", errClassificationMoved, current.Audience, current.DataClass)
		}
		if to.Audience == was.Audience && to.DataClass == was.DataClass {
			return nil
		}
		why, err := s.classificationRefusal(ctx, q, current, t, group, to)
		if err != nil {
			return err
		}
		if why != "" {
			return fault.New(fault.Conflict, why)
		}
		at := s.now()
		n, err := q.SetDeploymentClassification(ctx, deploymentsql.SetDeploymentClassificationParams{ID: current.ID, Audience: to.Audience, DataClass: to.DataClass, UpdatedAt: at, WasAudience: was.Audience, WasDataClass: was.DataClass})
		if err != nil {
			return err
		}
		if n != 1 {
			return errClassificationMoved
		}
		payload := map[string]any{"from": map[string]string{"audience": was.Audience, "data": was.DataClass}, "to": map[string]string{"audience": to.Audience, "data": to.DataClass},
			"protectedBefore": protectedAttributes(was), "protectedAfter": protectedAttributes(to)}
		kind := "deployment.classified"
		if lowering {
			kind = "deployment.classification-lowered"
			payload["meaning"] = in.Meaning
		}
		return audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, projectID, kind, name, payload)
	})
	if err != nil {
		return nil, err
	}
	return s.Deployments.ByID(ctx, dep.ID)
}
