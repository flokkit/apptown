package deployment

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// AddressSnapshot is the exact non-secret routing admitted for one placement.
// Target and revision bind it independently of mutable display labels.
type AddressSnapshot struct {
	DeploymentID ids.ID         `json:"deploymentId"`
	TargetID     ids.ID         `json:"targetId"`
	Revision     int64          `json:"revision"`
	Ingress      deploy.Ingress `json:"ingress"`
}

func (in AddressSnapshot) clone() AddressSnapshot { in.Ingress = in.Ingress.Clone(); return in }
func (in AddressSnapshot) same(other AddressSnapshot) bool {
	return in.DeploymentID == other.DeploymentID && in.TargetID == other.TargetID && in.Revision == other.Revision && in.Ingress.Same(other.Ingress)
}

// AddressReading distinguishes desired routing from the last observed serving
// URL. Retained claims include old hosts that remain reserved until teardown.
type AddressReading struct {
	DeploymentID      ids.ID          `json:"deploymentId"`
	TargetID          ids.ID          `json:"targetId"`
	GeneratedHostname string          `json:"generatedHostname"`
	CustomHostname    string          `json:"customHostname"`
	CertificateDomain string          `json:"certificateDomain"`
	Revision          int64           `json:"revision"`
	Verified          bool            `json:"verified"`
	ServingURL        string          `json:"servingURL"`
	ReservedHosts     []string        `json:"reservedHosts"`
	LegacyEvidence    json.RawMessage `json:"legacyEvidence,omitempty"`
}

func addressIngress(row deploymentsql.DeploymentAddressSetting) (deploy.Ingress, error) {
	if row.VerificationState != "ready" || row.Revision < 1 {
		return deploy.Ingress{}, fault.New(fault.Conflict, "inspect and confirm this deployment's existing provider addresses before releasing")
	}
	hosts := []string{row.GeneratedHostname}
	if row.CustomHostname != "" {
		hosts = append(hosts, row.CustomHostname)
	}
	for _, host := range hosts {
		if _, err := deploy.NormalizeHostname(host); err != nil || host != strings.TrimSpace(host) {
			return deploy.Ingress{}, fault.New(fault.Conflict, "recorded deployment address is invalid; inspect its provider before releasing")
		}
	}
	if _, err := deploy.NormalizeHostname(row.CertificateDomain); err != nil || row.CertificateDomain != strings.TrimSpace(row.CertificateDomain) {
		return deploy.Ingress{}, fault.New(fault.Conflict, "recorded certificate domain is invalid; inspect its provider before releasing")
	}
	return deploy.Ingress{Hosts: hosts, URL: "https://" + hosts[len(hosts)-1], CertificateDomain: row.CertificateDomain}, nil
}

func (s *Service) addressAuthority(ctx context.Context, projectID ids.ID, write bool) (access.Caller, error) {
	if s.configuration.Authority == nil || s.configuration.Projects == nil {
		return access.Caller{}, access.ErrAuthorityUnavailable
	}
	capability := access.CapDeploymentRead
	if write {
		capability = access.CapDeploymentBind
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: capability, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.address", Consequential: write}})
	if err != nil {
		return actor, err
	}
	if write && actor.Principal.Name == "" {
		return actor, access.ErrAuthorityUnavailable
	}
	return actor, nil
}

func addressTarget(target *Target) error {
	if target.Kind != KindPortainer {
		return fault.New(fault.Invalid, "this deployment's provider owns its address; managed hostname settings apply to Portainer")
	}
	return nil
}

// ReadAddress reads retained desired settings without observing or changing the
// provider. No settings row means no address has been selected yet.
func (s *Service) ReadAddress(ctx context.Context, projectID, deploymentID ids.ID) (AddressReading, error) {
	if _, err := s.addressAuthority(ctx, projectID, false); err != nil {
		return AddressReading{}, err
	}
	q := deploymentsql.New(s.db.SQL())
	dep, err := deploymentRecord(q.DeploymentByID(ctx, deploymentID))
	if err != nil {
		return AddressReading{}, err
	}
	if dep.ProjectID != projectID {
		return AddressReading{}, fault.New(fault.Invalid, "choose a deployment in this project")
	}
	target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
	if err != nil {
		return AddressReading{}, err
	}
	if err := addressTarget(target); err != nil {
		return AddressReading{}, err
	}
	out := AddressReading{DeploymentID: dep.ID, TargetID: target.ID, ServingURL: dep.URL, ReservedHosts: []string{}}
	row, err := q.DeploymentAddressSettings(ctx, dep.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.GeneratedHostname = row.GeneratedHostname
		out.CustomHostname = row.CustomHostname
		out.CertificateDomain = row.CertificateDomain
		out.Revision = row.Revision
		out.Verified = row.VerificationState == "ready"
		out.LegacyEvidence = slices.Clone(row.LegacyEvidenceJson)
	}
	hosts, err := q.DeploymentHostnameClaims(ctx, dep.ID)
	if err != nil {
		return out, err
	}
	out.ReservedHosts = append(out.ReservedHosts, hosts...)
	return out, nil
}

// EditAddress records desired settings and claims; traffic moves only on a
// subsequent admitted release. A stale edit or failed receipt changes nothing.
func (s *Service) EditAddress(ctx context.Context, projectID, deploymentID ids.ID, expectedRevision int64, customHostname string) (AddressSnapshot, error) {
	actor, err := s.addressAuthority(ctx, projectID, true)
	if err != nil {
		return AddressSnapshot{}, err
	}
	if expectedRevision < 0 {
		return AddressSnapshot{}, fault.New(fault.Invalid, "address revision must not be negative")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (AddressSnapshot, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return AddressSnapshot{}, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return AddressSnapshot{}, err
		}
		q := deploymentsql.New(tx.SQL())
		dep, err := deploymentRecord(q.DeploymentByID(ctx, deploymentID))
		if err != nil {
			return AddressSnapshot{}, err
		}
		if dep.ProjectID != projectID {
			return AddressSnapshot{}, fault.New(fault.Invalid, "choose a deployment in this project")
		}
		target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
		if err != nil {
			return AddressSnapshot{}, err
		}
		target, err = targetRecord(q.TargetByNameForUpdate(ctx, target.Name))
		if err != nil {
			return AddressSnapshot{}, err
		}
		dep, err = deploymentRecord(q.DeploymentForUpdate(ctx, dep.ID))
		if err != nil {
			return AddressSnapshot{}, err
		}
		if err := addressTarget(target); err != nil {
			return AddressSnapshot{}, err
		}
		if err := s.defaultEligible(ctx, q, projectID, dep, target); err != nil {
			return AddressSnapshot{}, err
		}
		snapshot, changed, err := s.addressSelectionIn(ctx, q, dep, target, &expectedRevision, &customHostname)
		if err != nil {
			return AddressSnapshot{}, err
		}
		if changed {
			if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.address-edited", dep.Name, snapshot); err != nil {
				return AddressSnapshot{}, err
			}
		}
		return snapshot, nil
	})
}

// addressSelectionIn participates in the caller's project/placement admission
// transaction. Nil selection preserves the existing input; missing records get
// a UUID-derived generated host once. It never rewrites legacy evidence.
func (s *Service) addressSelectionIn(ctx context.Context, q *deploymentsql.Queries, dep *Deployment, target *Target, expected *int64, custom *string) (AddressSnapshot, bool, error) {
	var snapshot AddressSnapshot
	row, err := q.DeploymentAddressSettingsForUpdate(ctx, dep.ID)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return snapshot, false, err
	}
	if expected != nil && ((fresh && *expected != 0) || (!fresh && row.Revision != *expected)) {
		return snapshot, false, fault.New(fault.Conflict, "deployment address changed; reload before editing")
	}
	if fresh {
		domain, _ := target.Ingress["domain"].(string)
		domain, err = deploy.NormalizeHostname(domain)
		if err != nil {
			return snapshot, false, fault.New(fault.Invalid, "the target needs a valid applications domain")
		}
		generated, err := deploy.NormalizeHostname("app-" + strings.ReplaceAll(dep.ID.String(), "-", "") + "." + domain)
		if err != nil {
			return snapshot, false, fault.New(fault.Invalid, "the target domain is too long for a deployment address")
		}
		row = deploymentsql.DeploymentAddressSetting{DeploymentID: dep.ID, GeneratedHostname: generated, CertificateDomain: domain, VerificationState: "ready", Revision: 1}
	} else if row.VerificationState != "ready" {
		return snapshot, false, fault.New(fault.Conflict, "inspect and confirm this deployment's existing provider addresses before editing or releasing")
	}
	oldCustom := row.CustomHostname
	if custom != nil {
		row.CustomHostname = strings.TrimSpace(*custom)
		if row.CustomHostname != "" {
			row.CustomHostname, err = deploy.NormalizeHostname(row.CustomHostname)
			if err != nil {
				return snapshot, false, fault.New(fault.Invalid, err.Error())
			}
		}
		generated, _ := deploy.NormalizeHostname(row.GeneratedHostname)
		if row.CustomHostname == generated {
			row.CustomHostname = ""
		}
		label, _, _ := strings.Cut(row.CustomHostname, ".")
		if strings.HasPrefix(label, "app-") && len(label) == 36 {
			if _, err := hex.DecodeString(label[4:]); err == nil {
				return snapshot, false, fault.New(fault.Invalid, "UUID-generated hostnames are reserved; choose a customer hostname")
			}
		}
	}
	ingress, err := addressIngress(row)
	if err != nil {
		return snapshot, false, err
	}
	if err := reserveAddressHosts(ctx, q, dep.ID, ingress, row.CustomHostname, false); err != nil {
		return snapshot, false, err
	}
	changed := fresh || row.CustomHostname != oldCustom
	if fresh {
		row, err = q.InsertDeploymentAddressSettings(ctx, deploymentsql.InsertDeploymentAddressSettingsParams{DeploymentID: dep.ID, GeneratedHostname: row.GeneratedHostname, CustomHostname: row.CustomHostname, CertificateDomain: row.CertificateDomain})
	} else if changed {
		row, err = q.UpdateDeploymentAddressSettings(ctx, deploymentsql.UpdateDeploymentAddressSettingsParams{DeploymentID: dep.ID, ExpectedRevision: row.Revision, GeneratedHostname: row.GeneratedHostname, CustomHostname: row.CustomHostname, CertificateDomain: row.CertificateDomain})
	}
	if err != nil {
		return snapshot, false, err
	}
	return AddressSnapshot{DeploymentID: dep.ID, TargetID: target.ID, Revision: row.Revision, Ingress: ingress.Clone()}, changed, nil
}

func reserveAddressHosts(ctx context.Context, q *deploymentsql.Queries, deploymentID ids.ID, ingress deploy.Ingress, custom string, observed bool) error {
	keys := make([]string, 0, len(ingress.Hosts))
	for _, host := range ingress.Hosts {
		key, err := deploy.NormalizeHostname(host)
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	customKey := ""
	if custom != "" {
		var err error
		customKey, err = deploy.NormalizeHostname(custom)
		if err != nil {
			return err
		}
	}
	for _, key := range keys {
		if err := q.EnsureHostnameClaim(ctx, key); err != nil {
			return err
		}
		if _, err := q.LockHostnameClaim(ctx, key); err != nil {
			return err
		}
		holders, err := q.HostnameClaimants(ctx, key)
		if err != nil {
			return err
		}
		owns := slices.Contains(holders, deploymentID)
		if !observed {
			for _, holder := range holders {
				if holder != deploymentID {
					return fault.New(fault.Conflict, "hostname is already reserved; choose another address or ask an administrator to inspect the existing reservation")
				}
			}
		}
		if !observed && key == customKey && !owns {
			unverified, err := q.HasUnverifiedAddressInventory(ctx)
			if err != nil {
				return err
			}
			if unverified {
				return fault.New(fault.Conflict, "existing provider address inventories need confirmation before a new custom hostname can be reserved")
			}
		}
		if err := q.AddHostnameClaimant(ctx, deploymentsql.AddHostnameClaimantParams{Hostname: key, DeploymentID: deploymentID}); err != nil {
			return err
		}
	}
	return nil
}

// releaseAddressClaimsIn is called only by confirmed removal settlement. Foreign
// key restrictions prevent a different deletion path from freeing held hosts.
func releaseAddressClaimsIn(ctx context.Context, q *deploymentsql.Queries, deploymentID ids.ID) error {
	hosts, err := q.DeploymentHostnameClaims(ctx, deploymentID)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if _, err := q.LockHostnameClaim(ctx, host); err != nil {
			return err
		}
	}
	if err := q.RemoveDeploymentHostnameClaims(ctx, deploymentID); err != nil {
		return err
	}
	for _, host := range hosts {
		if err := q.RemoveEmptyHostnameClaim(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func addressSnapshotJSON(snapshot *AddressSnapshot) (json.RawMessage, error) {
	if snapshot == nil {
		return nil, nil
	}
	if err := snapshot.valid(); err != nil {
		return nil, err
	}
	return json.Marshal(snapshot)
}

func addressSnapshotFrom(raw json.RawMessage) (*AddressSnapshot, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var snapshot AddressSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.valid(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (in AddressSnapshot) valid() error {
	if in.DeploymentID.IsZero() || in.TargetID.IsZero() || in.Revision < 1 || len(in.Ingress.Hosts) < 1 || len(in.Ingress.Hosts) > 2 {
		return fault.New(fault.Conflict, "admitted deployment addresses are incomplete; request a new release")
	}
	seen := map[string]bool{}
	for _, host := range in.Ingress.Hosts {
		key, err := deploy.NormalizeHostname(host)
		if err != nil || host != strings.TrimSpace(host) || seen[key] {
			return fault.New(fault.Conflict, "admitted deployment addresses are malformed; request a new release")
		}
		seen[key] = true
	}
	domain, err := deploy.NormalizeHostname(in.Ingress.CertificateDomain)
	first, _ := deploy.NormalizeHostname(in.Ingress.Hosts[0])
	if err != nil || in.Ingress.CertificateDomain != strings.TrimSpace(in.Ingress.CertificateDomain) || !strings.HasSuffix(first, "."+domain) || in.Ingress.URL != "https://"+in.Ingress.Hosts[len(in.Ingress.Hosts)-1] {
		return fault.New(fault.Conflict, "admitted deployment routing is inconsistent; request a new release")
	}
	return nil
}

// admitAddressIn shares the release admission transaction. A refusal or failed
// job/receipt rolls back both the desired address and its reservations.
func (s *Service) admitAddressIn(ctx context.Context, tx *database.Tx, dep *Deployment, target *Target, expected *int64, custom *string, actor string) (*AddressSnapshot, error) {
	if target.Kind != KindPortainer {
		if custom != nil || expected != nil {
			return nil, fault.New(fault.Invalid, "this provider owns its deployment address")
		}
		return nil, nil
	}
	snapshot, changed, err := s.addressSelectionIn(ctx, deploymentsql.New(tx.SQL()), dep, target, expected, custom)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor, dep.ProjectID, "deployment.address-admitted", dep.Name, snapshot); err != nil {
			return nil, err
		}
	}
	return &snapshot, nil
}

// checkAddressSnapshot refuses stale or incomplete queued input before provider
// preparation. A target-domain edit does not recompute an admitted address.
func (s *Service) checkAddressSnapshot(ctx context.Context, dep *Deployment, target *Target, snapshot *AddressSnapshot) error {
	if target.Kind != KindPortainer {
		if snapshot != nil {
			return ErrPlacementChanged
		}
		return nil
	}
	if snapshot == nil {
		return fault.New(fault.Conflict, "this pending release has no admitted deployment address; request a new release")
	}
	if err := snapshot.valid(); err != nil {
		return err
	}
	if snapshot.DeploymentID != dep.ID || snapshot.TargetID != target.ID {
		return ErrPlacementChanged
	}
	q := deploymentsql.New(s.db.SQL())
	row, err := q.DeploymentAddressSettings(ctx, dep.ID)
	if err != nil {
		return err
	}
	ingress, err := addressIngress(row)
	if err != nil {
		return err
	}
	if row.Revision != snapshot.Revision || !ingress.Same(snapshot.Ingress) {
		return fault.New(fault.Conflict, "deployment address changed after admission; request a new release")
	}
	for _, host := range ingress.Hosts {
		key, err := deploy.NormalizeHostname(host)
		if err != nil {
			return err
		}
		holders, err := q.HostnameClaimants(ctx, key)
		if err != nil {
			return err
		}
		if len(holders) != 1 || holders[0] != dep.ID {
			return fault.New(fault.Conflict, "deployment address reservation needs attention before releasing")
		}
	}
	return nil
}
