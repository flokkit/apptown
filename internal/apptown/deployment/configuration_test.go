package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"reflect"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type configurationIdentity struct{ Name, ProjectSlug, OwnerGroup string }
type configurationWorld struct {
	actor       configurationIdentity
	denial      error
	contract    *appcontract.Contract
	contractErr error
	before      func()
}

func (w *configurationWorld) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: w.actor.Name}, Verified: true, Receipt: w.actor.Name}, w.denial
}
func (w *configurationWorld) Project(_ context.Context, id ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: id, Slug: w.actor.ProjectSlug}, OwnerGroup: w.actor.OwnerGroup}, nil
}
func (w *configurationWorld) CurrentContract(context.Context, string) (*appcontract.Contract, error) {
	if w.before != nil {
		w.before()
	}
	return w.contract, w.contractErr
}
func configurationFixture(t *testing.T) (*Service, *configurationWorld, ConfigureDeployment, *Target) {
	t.Helper()
	db := testDB(t)
	// Project is an external prerequisite; this suite owns only Deployment setup.
	project := createTestProject(t, db, "config", "Configuration")
	w := &configurationWorld{actor: configurationIdentity{Name: "alice", ProjectSlug: "config", OwnerGroup: "team"}, contract: &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}}}
	s := New(context.Background(), db, Dependencies{Authority: w, Configuration: ConfigurationDependencies{Authority: w, Projects: w, Contracts: w}})
	target, err := s.Targets.Put(context.Background(), &Target{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady, NeedsMode: "managed", Capabilities: TargetCapabilities{Needs: []string{"postgres"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(context.Background(), target, "*", OfferCaps{}, "setup"); err != nil {
		t.Fatal(err)
	}
	return s, w, ConfigureDeployment{ProjectID: project, Name: "prod", Target: target.Name}, target
}
func configureForTest(t *testing.T, s *Service, in ConfigureDeployment) *Deployment {
	t.Helper()
	result, err := s.Configure(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return result.Deployment
}
func TestConfigureDefaultsAndContractAssessment(t *testing.T) {
	for _, scenario := range []string{"compatible", "incompatible", "missing", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, w, in, target := configurationFixture(t)
			switch scenario {
			case "incompatible":
				w.contract.Parts[0].DataDir = "/data"
			case "missing":
				w.contract = nil
			case "unreadable":
				w.contractErr = errors.New("cannot read contract")
			}
			result, err := s.Configure(context.Background(), in)
			if err != nil || result.Deployment == nil {
				t.Fatalf("configuration: %+v %v", result, err)
			}
			dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.Name)
			if err != nil {
				t.Fatal(err)
			}
			if dep.DeploymentPolicy != CompatibleUpdate || dep.TargetID != target.ID || dep.Feed != "promotion" || dep.Audience != "team" || dep.DataClass != "non-production" {
				t.Fatalf("defaults: %+v", dep)
			}
			if result.ContractChecked != (scenario == "compatible" || scenario == "incompatible") || (result.ContractIssue == "") != (scenario == "compatible") {
				t.Fatalf("assessment: %+v", result)
			}
			wantN(t, "bound receipts", receipts(t, s.db, "deployment.bound", byActor("alice"), byProject(in.ProjectID)), 1)
		})
	}
}
func TestConfigureRefusals(t *testing.T) {
	cases := []string{"denied", "missing caller", "bad name", "preview name", "bad feed", "bad audience", "bad data", "missing target", "unreachable", "retired", "no offer", "data cap", "audience cap", "group overrides everyone", "malformed caps", "cancelled"}
	for _, scenario := range cases {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, w, in, target := configurationFixture(t)
			ctx := context.Background()
			switch scenario {
			case "denied":
				w.denial = errors.New("denied")
			case "missing caller":
				w.actor.Name = ""
			case "bad name":
				in.Name = "Preview!"
			case "preview name":
				in.Name = "preview"
			case "bad feed":
				in.Feed = "invalid"
			case "bad audience":
				in.Audience = "invalid"
			case "bad data":
				in.DataClass = "invalid"
			case "missing target":
				in.Target = "absent"
			case "unreachable":
				target.Status = TargetUnreachable
				if _, err := s.Targets.Put(ctx, target); err != nil {
					t.Fatal(err)
				}
			case "retired":
				if err := s.Targets.Retire(ctx, target.Name, "setup"); err != nil {
					t.Fatal(err)
				}
			case "no offer":
				offers, err := s.Targets.Offers(ctx, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Targets.RevokeOffer(ctx, target, offers[0].ID, "setup"); err != nil {
					t.Fatal(err)
				}
			case "data cap":
				if err := s.Targets.Offer(ctx, target, "*", OfferCaps{Data: []string{"synthetic"}}, "setup"); err != nil {
					t.Fatal(err)
				}
			case "audience cap":
				if err := s.Targets.Offer(ctx, target, "*", OfferCaps{Audiences: []string{"team"}}, "setup"); err != nil {
					t.Fatal(err)
				}
				in.Audience = "public"
			case "group overrides everyone":
				if err := s.Targets.Offer(ctx, target, "team", OfferCaps{Data: []string{"synthetic"}}, "setup"); err != nil {
					t.Fatal(err)
				}
			case "malformed caps":
				if _, err := deploymentsql.New(s.db.SQL()).PutOffer(ctx, deploymentsql.PutOfferParams{ID: ids.New(time.Now().UTC()), TargetID: target.ID, GroupName: "*", CapsJson: []byte(`{"maxStanding":"bad"}`), CreatedBy: "setup", CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := s.Configure(ctx, in)
			if err == nil || result.Deployment != nil {
				t.Fatalf("refusal admitted: %+v %v", result, err)
			}
			var input *ConfigurationInputError
			var denied *OfferDeniedError
			if strings.HasPrefix(scenario, "bad ") || scenario == "preview name" {
				if !errors.As(err, &input) {
					t.Fatalf("input classification: %v", err)
				}
			}
			if scenario == "no offer" || strings.Contains(scenario, " cap") && scenario != "malformed caps" || scenario == "group overrides everyone" {
				if !errors.As(err, &denied) {
					t.Fatalf("offer classification: %v", err)
				}
			}
			if scenario == "missing target" && !errors.Is(err, ErrTargetMissing) {
				t.Fatal(err)
			}
			wantN(t, "deployments", everyDeployment(t, s.db), 0)
			wantN(t, "bound receipts", receipts(t, s.db, "deployment.bound"), 0)
		})
	}
}
func TestConfigureImplicitOffersAndGroupSelection(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		t.Run(fmt.Sprint(implicit), func(t *testing.T) {
			t.Parallel()
			s, _, in, target := configurationFixture(t)
			if implicit {
				var err error
				target, err = s.Targets.Put(context.Background(), &Target{Name: "built-in", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
				if err != nil {
					t.Fatal(err)
				}
				in.Target = target.Name
			} else {
				if err := s.Targets.Offer(context.Background(), target, "team", OfferCaps{Data: []string{"production"}}, "setup"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Targets.Offer(context.Background(), target, "*", OfferCaps{Data: []string{"synthetic"}}, "setup"); err != nil {
				t.Fatal(err)
			}
			in.DataClass = "production"
			if !configureForTest(t, s, in).Protected() {
				t.Fatal("production data must be protected")
			}
		})
	}
}
func TestConfigureLimitIsAtomicAndExcludesCurrentBinding(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	if err := s.Targets.Offer(context.Background(), target, "*", OfferCaps{MaxStanding: 1}, "setup"); err != nil {
		t.Fatal(err)
	}
	outcomes := race(8, func(i int) int {
		request := in
		request.Name = fmt.Sprintf("env%d", i)
		_, err := s.Configure(context.Background(), request)
		if err == nil {
			return 1
		}
		var refusal *OfferDeniedError
		if !errors.As(err, &refusal) {
			t.Error(err)
		}
		return 0
	})
	successes := 0
	for _, n := range outcomes {
		successes += n
	}
	if successes != 1 {
		t.Fatalf("accepted %d", successes)
	}
	envs, err := s.Deployments.ForProject(t.Context(), in.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = envs[0].Name
	in.Audience = "organisation"
	dep := configureForTest(t, s, in)
	if dep.ID != envs[0].ID || dep.Audience != in.Audience {
		t.Fatalf("update: %+v", dep)
	}
	wantN(t, "bound receipts", receipts(t, s.db, "deployment.bound"), 2)
}
func TestConfigureBindingAndReceiptRollback(t *testing.T) {
	for _, table := range []string{"deployments", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			s, _, in, _ := configurationFixture(t)
			mustExec(t, s.db, `CREATE FUNCTION veto_configuration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			mustExec(t, s.db, `CREATE TRIGGER veto_configuration BEFORE INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_configuration()`)
			result, err := s.Configure(context.Background(), in)
			if err == nil || result.Deployment != nil {
				t.Fatalf("suppressed receipt: %+v %v", result, err)
			}
			wantN(t, "deployments", everyDeployment(t, s.db), 0)
			wantN(t, "bound receipts", receipts(t, s.db, "deployment.bound"), 0)
			mustExec(t, s.db, `DROP TRIGGER veto_configuration ON `+table)
			configureForTest(t, s, in)
		})
	}
}
func TestConfigureDoesNotRetargetAdmittedDeployment(t *testing.T) {
	t.Parallel()
	s, _, in, target := configurationFixture(t)
	dep := configureForTest(t, s, in)
	if _, _, err := s.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "admitted"}); err != nil {
		t.Fatal(err)
	}
	other, err := s.Targets.Put(context.Background(), &Target{Name: "other", Kind: KindDOApp, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	in.Target = other.Name
	if _, err := s.Configure(context.Background(), in); !errors.Is(err, ErrDeploymentAdmitted) {
		t.Fatalf("retargeted admitted generation: %v", err)
	}
	in.Target = target.Name
	in.Audience = "public"
	if _, err := s.Configure(context.Background(), in); !errors.Is(err, ErrDeploymentAdmitted) {
		t.Fatalf("changed protection: %v", err)
	}
	in.Audience = "organisation"
	updated := configureForTest(t, s, in)
	if updated.TargetID != target.ID || updated.DesiredGeneration != 1 {
		t.Fatalf("generation changed: %+v", updated)
	}
}

func TestConfigureUsesOfferAndTargetCurrentAtCommit(t *testing.T) {
	for _, change := range []string{"offer withdrawn", "caps reduced", "target retired"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, w, in, target := configurationFixture(t)
			w.before = func() {
				switch change {
				case "offer withdrawn":
					offers, err := s.Targets.Offers(context.Background(), target.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.Targets.RevokeOffer(context.Background(), target, offers[0].ID, "setup"); err != nil {
						t.Fatal(err)
					}
				case "caps reduced":
					if err := s.Targets.Offer(context.Background(), target, "*", OfferCaps{Data: []string{"synthetic"}}, "setup"); err != nil {
						t.Fatal(err)
					}
				case "target retired":
					if err := s.Targets.Retire(context.Background(), target.Name, "setup"); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := s.Configure(context.Background(), in)
			if err == nil || result.Deployment != nil {
				t.Fatalf("used stale target/offer: %+v %v", result, err)
			}
			wantN(t, "deployments", everyDeployment(t, s.db), 0)
		})
	}
}

func TestConfigureFailedUpdateKeepsPriorSettings(t *testing.T) {
	t.Parallel()
	s, _, in, _ := configurationFixture(t)
	original := configureForTest(t, s, in)
	dbtest.FailNextStatement(t, s.db, "INSERT INTO audit_log")
	in.Audience = "public"
	result, err := s.Configure(context.Background(), in)
	if err == nil || result.Deployment != nil {
		t.Fatalf("missing update receipt: %+v %v", result, err)
	}
	current, err := s.Deployments.ByID(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Audience != original.Audience || current.UpdatedAt != original.UpdatedAt {
		t.Fatalf("failed update changed settings: %+v", current)
	}
	wantN(t, "bound receipts", receipts(t, s.db, "deployment.bound"), 1)
}

// Unsupported choices must not silently configure a different procedure.
// Reconfiguration preserves the recorded placement, and source work is never
// reached for a refused procedure; authority refusals still take precedence.
func TestDeploymentPolicyRefusedBeforeConfigurationEffects(t *testing.T) {
	for _, value := range []DeploymentPolicy{ProtectedMaintenance, "other", "Compatible-Update", " compatible-update"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", value, existing), func(t *testing.T) {
				t.Parallel()
				s, w, in, _ := configurationFixture(t)
				var before *Deployment
				if existing {
					before = configureForTest(t, s, in)
				}
				n := len(receipts(t, s.db, "deployment.bound"))
				w.before = func() { t.Error("policy refusal reached source contract preparation") }
				in.DeploymentPolicy = value
				in.Audience = "public"
				result, err := s.Configure(t.Context(), in)
				if err == nil || result.Deployment != nil {
					t.Fatalf("unavailable procedure configured: %+v %v", result, err)
				}
				want := fault.Invalid
				if value == ProtectedMaintenance {
					want = fault.Unprocessable
					if fault.CodeOf(err) != "deployment_policy_not_implemented" || !strings.Contains(err.Error(), "use compatible-update") {
						t.Fatalf("reserved refusal: %v", err)
					}
				}
				if fault.KindOf(err) != want {
					t.Fatalf("policy classification: %v", err)
				}
				rows, err := s.Deployments.ForProject(t.Context(), in.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					if len(rows) != 1 || !reflect.DeepEqual(rows[0], before) {
						t.Fatalf("refusal changed settings: %+v", rows)
					}
				} else if len(rows) != 0 {
					t.Fatalf("refusal created settings: %+v", rows)
				}
				if len(receipts(t, s.db, "deployment.bound")) != n {
					t.Fatal("policy refusal wrote configuration receipt")
				}
				w.denial = errors.New("no binding authority")
				if _, err := s.Configure(t.Context(), in); err != w.denial {
					t.Fatalf("policy leaked before authority refusal: %v", err)
				}
			})
		}
	}
}

func TestStandingDeploymentPolicyDefaultsAreExplicit(t *testing.T) {
	t.Parallel()
	s, _, in, _ := configurationFixture(t)
	for _, value := range []DeploymentPolicy{"", CompatibleUpdate, ""} {
		in.DeploymentPolicy = value
		dep := configureForTest(t, s, in)
		if dep.DeploymentPolicy != CompatibleUpdate || dep.UpdatePolicy() != CompatibleUpdate {
			t.Fatalf("default policy: %+v", dep)
		}
		rows, err := s.Deployments.ByID(t.Context(), dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rows.DeploymentPolicy != CompatibleUpdate {
			t.Fatalf("record read default: %+v", rows)
		}
	}
	in.Name = "explicit-new"
	in.DeploymentPolicy = CompatibleUpdate
	if dep := configureForTest(t, s, in); dep.DeploymentPolicy != CompatibleUpdate {
		t.Fatalf("explicit new default: %+v", dep)
	}
	entries := receipts(t, s.db, "deployment.bound")
	for _, entry := range entries {
		var payload map[string]any
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["deploymentPolicy"] != string(CompatibleUpdate) {
			t.Fatalf("configured procedure receipt: %+v", payload)
		}
	}
	for _, kind := range []Kind{Preview, Restored} {
		d := Deployment{Kind: kind}
		if d.UpdatePolicy() != "" {
			t.Fatalf("non-standing placement claims a selected procedure: %+v", d)
		}
	}
}

// Existing stored records gain the implemented default without a migration;
// preview and restored-copy records omit an update-policy claim entirely.
func TestDeploymentPolicyReadDefaultsExcludeTemporaryCopies(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{Standing, Preview, Restored} {
		d, err := deploymentRecord(deploymentsql.Deployment{Kind: kind.String(), Readiness: Reconciled.String()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err = json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if kind == Standing {
			if view["deploymentPolicy"] != "compatible-update" {
				t.Fatalf("standing record default: %s", raw)
			}
		} else if _, ok := view["deploymentPolicy"]; ok {
			t.Fatalf("temporary copy claims selected update policy: %s", raw)
		}
	}
}
