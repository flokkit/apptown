package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// TargetSettingsEdit changes the settings a person may edit on a target.
// Omitted fields keep their values. ExpectedRevision is the settings
// revision the edit started from; it is required.
type TargetSettingsEdit struct {
	ExpectedRevision  *int64
	Domain            *string
	PersistentVolumes *bool
	Previews          *bool
	NeedsMode         *string
	// Evals marks an attached Docker host for evals: its domain is then the
	// eval domain, and it takes eval copies only (evals.go).
	Evals *bool
	// Rehearsals keeps an attached Docker host for restore rehearsals: it
	// then takes rehearsals and nothing else, and production data sits on it
	// while one runs (RehearsalHostMeaning, recorded with the edit).
	Rehearsals *bool
	// CopyRunner names the Docker host where the apptown runs the throwaway
	// containers that copy an App Platform target's managed data ("" for
	// none): the copy runner (design/subsystems/deployment.md, "Protecting
	// data off-host").
	CopyRunner *string
}

var (
	ErrTargetSettingsRevisionRequired = fault.New(fault.Invalid, "send the settings revision the edit started from (expectedRevision)")
	ErrTargetEditEmpty                = fault.New(fault.Invalid, "nothing to change: send domain, persistentVolumes, previews, needsMode, evals, rehearsals or copyRunner")
	ErrTargetRehearsals               = fault.New(fault.Invalid, "only an attached Docker host can be kept for restore rehearsals, and not one kept for evals")
	ErrTargetCopyRunner               = fault.New(fault.Invalid, "a copy runner is a Docker host target AppTown reaches, named on an App Platform target")
	ErrTargetEvals                    = fault.New(fault.Invalid, "only an attached Docker host with a domain can be kept for evals: its domain is the eval domain")
	ErrTargetDomain                   = fault.New(fault.Invalid, "the domain must be a bare hostname, such as apps.example.com")
	ErrTargetNeedsMode                = fault.New(fault.Invalid, "needsMode must be emulators, managed or register")
	// ErrTargetSettingsMoved carries a code so a client can tell it from other
	// conflicts and offer the person the current settings.
	ErrTargetSettingsMoved error = targetSettingsMoved{}
)

// AddressCopyRunner is where a target's record names its copy runner.
const AddressCopyRunner = "copyRunner"

// CopyRunner is the Docker host named to run this target's copy jobs, ""
// when none is.
func (t *Target) CopyRunner() string {
	name, _ := t.Address[AddressCopyRunner].(string)
	return name
}

// A target's record holds two kinds of fact. Its configuration comes from
// where the target is declared — AppTown itself for the built-in host, the
// connector for an App Platform account, AppTown configuration for the
// intranet's Portainer — and Targets.Sync refreshes it. Its settings are what
// an administrator states through EditSettings, and only these: the domain,
// evals and rehearsals marks in ingress, persistentVolumes and previews in
// capabilities, the needs mode, and the copy runner in address. A sync
// supplies a setting's first value when it creates the target; after that
// the setting is the administrator's and no sync changes it. The built-in
// host takes no edits, so every fact of its record is AppTown's own.
var (
	settingIngressKeys = []string{"domain", IngressEvals, IngressRehearsals}
	settingAddressKeys = []string{AddressCopyRunner}
)

// keepSettings carries the stored record's settings into t, the record a
// sync proposes: a setting the stored record lacks stays absent.
func (t *Target) keepSettings(stored *Target) {
	if stored.Origin == OriginBuiltIn {
		return
	}
	t.Address = withKeysOf(t.Address, stored.Address, settingAddressKeys)
	t.Ingress = withKeysOf(t.Ingress, stored.Ingress, settingIngressKeys)
	t.Capabilities.PersistentVolumes, t.Capabilities.Previews = stored.Capabilities.PersistentVolumes, stored.Capabilities.Previews
	t.NeedsMode = stored.NeedsMode
}

// withKeysOf is a copy of m whose keys listed take from's values, or are
// absent where from lacks them.
func withKeysOf(m, from map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(m)+len(keys))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		if v, ok := from[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return out
}

type targetSettingsMoved struct{}

func (targetSettingsMoved) Error() string {
	return "the target's settings changed since this edit started; review the current values before saving again"
}
func (targetSettingsMoved) FaultKind() fault.Kind { return fault.Conflict }
func (targetSettingsMoved) FaultCode() string     { return "settings-moved" }

// EditSettings applies a person's edit of a target's settings under the
// target's row lock: the built-in host and a retired target are refused, the
// edit must name the current settings revision, only the fields it sends
// change, and the revision advances with the write. The audit receipt
// commits with it. Status checks write only the status, so they never make
// an edit conflict.
func (s *Targets) EditSettings(ctx context.Context, name string, in TargetSettingsEdit, who string) (*Target, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		q := deploymentsql.New(tx.SQL())
		// The targets lock order: table, then row (see Targets).
		if err := q.LockTargets(ctx); err != nil {
			return nil, err
		}
		t, err := targetRecord(q.TargetByNameForUpdate(ctx, name))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.NotFound, fmt.Sprintf("no target named %q", name))
		}
		if err != nil {
			return nil, err
		}
		if t.Origin == OriginBuiltIn {
			return nil, fault.New(fault.Conflict, fmt.Sprintf("%s is this AppTown's own host; its facts are its own", t.Name))
		}
		if err := t.Status.Available(); err != nil {
			return nil, fault.New(fault.Conflict, fmt.Sprintf("%s is retired", t.Name))
		}
		if in.ExpectedRevision == nil {
			return nil, ErrTargetSettingsRevisionRequired
		}
		if *in.ExpectedRevision != t.SettingsRevision {
			return nil, ErrTargetSettingsMoved
		}
		before, after := map[string]any{}, map[string]any{}
		if in.Domain != nil {
			d := strings.TrimSpace(strings.TrimPrefix(*in.Domain, "*."))
			if d != "" {
				if _, err := url.Parse("https://" + d); err != nil || strings.ContainsAny(d, "/ ") {
					return nil, ErrTargetDomain
				}
			}
			if t.Ingress == nil {
				t.Ingress = map[string]any{}
			}
			before["domain"], after["domain"] = t.Ingress["domain"], d
			if d == "" {
				delete(t.Ingress, "domain")
			} else {
				t.Ingress["domain"] = d
			}
		}
		if in.PersistentVolumes != nil {
			before["persistentVolumes"], after["persistentVolumes"] = t.Capabilities.PersistentVolumes, *in.PersistentVolumes
			t.Capabilities.PersistentVolumes = *in.PersistentVolumes
		}
		if in.Previews != nil {
			before["previews"], after["previews"] = t.Capabilities.Previews, *in.Previews
			t.Capabilities.Previews = *in.Previews
		}
		if in.NeedsMode != nil {
			m := strings.TrimSpace(*in.NeedsMode)
			if m != "emulators" && m != "managed" && m != "register" {
				return nil, ErrTargetNeedsMode
			}
			before["needsMode"], after["needsMode"] = t.NeedsMode, m
			t.NeedsMode = m
		}
		if in.Evals != nil {
			if t.Ingress == nil {
				t.Ingress = map[string]any{}
			}
			before["evals"], after["evals"] = t.ForEvals(), *in.Evals
			if *in.Evals {
				t.Ingress[IngressEvals] = true
			} else {
				delete(t.Ingress, IngressEvals)
			}
		}
		if in.Rehearsals != nil {
			if t.Ingress == nil {
				t.Ingress = map[string]any{}
			}
			before["rehearsals"], after["rehearsals"] = t.ForRehearsals(), *in.Rehearsals
			if *in.Rehearsals {
				t.Ingress[IngressRehearsals] = true
				// The meaning the administrator was shown is kept with the
				// mark, in the edit's receipt.
				after["meaning"] = RehearsalHostMeaning
			} else {
				delete(t.Ingress, IngressRehearsals)
			}
		}
		if in.CopyRunner != nil {
			name := strings.TrimSpace(*in.CopyRunner)
			if t.Kind != KindDOApp {
				return nil, ErrTargetCopyRunner
			}
			if name != "" {
				runner, err := targetRecord(q.TargetByName(ctx, name))
				if err != nil || runner.Kind != KindDockerHost || runner.Status.Available() != nil {
					return nil, fmt.Errorf("%w: %s is not an available Docker host target", ErrTargetCopyRunner, name)
				}
			}
			if t.Address == nil {
				t.Address = map[string]any{}
			}
			before["copyRunner"], after["copyRunner"] = t.Address[AddressCopyRunner], name
			if name == "" {
				delete(t.Address, AddressCopyRunner)
			} else {
				t.Address[AddressCopyRunner] = name
			}
		}
		if domain, _ := t.Ingress["domain"].(string); t.Ingress[IngressEvals] == true && (t.Kind != KindDockerHost || domain == "") {
			return nil, ErrTargetEvals
		}
		if t.Ingress[IngressRehearsals] == true && (t.Kind != KindDockerHost || t.Origin != OriginAttached || t.Ingress[IngressEvals] == true) {
			return nil, ErrTargetRehearsals
		}
		if len(after) == 0 {
			return nil, ErrTargetEditEmpty
		}
		saved, err := targetRecord(q.EditTargetSettings(ctx, deploymentsql.EditTargetSettingsParams{
			ID: t.ID, CapabilitiesJson: json.RawMessage(jsonText(t.Capabilities)), IngressJson: json.RawMessage(jsonText(t.Ingress)),
			NeedsMode: t.NeedsMode, AddressJson: json.RawMessage(jsonText(t.Address)), UpdatedAt: now,
		}))
		if err != nil {
			return nil, err
		}
		if err := audit.At(now).RecordIn(ctx, tx, who, ids.Nil, "target.edited", saved.Name, map[string]any{"before": before, "after": after}); err != nil {
			return nil, err
		}
		return saved, nil
	})
}

// IngressRehearsals is the ingress key marking an attached Docker host kept
// for restore rehearsals: it takes rehearsals and nothing else.
const IngressRehearsals = "rehearsals"

// RehearsalHostMeaning is what marking a host for rehearsals means, shown to
// the platform administrator who marks it and kept with the mark.
const RehearsalHostMeaning = "Production data will sit on this host while a restore rehearsal runs. Anyone who controls this host can read it: root, anyone with its Docker socket, any process on it and any container on its network. Keep it for rehearsals and nothing else."

// ForRehearsals says whether the target is an attached Docker host kept for
// restore rehearsals.
func (t *Target) ForRehearsals() bool {
	marked, _ := t.Ingress[IngressRehearsals].(bool)
	return marked && t.Origin == OriginAttached && t.Kind == KindDockerHost
}

// Reserved is the one purpose a host is kept for ("evals", "rehearsals"),
// "" for a host that takes ordinary deployments. A kept host takes only
// that purpose (refusesPlacement).
func (t *Target) Reserved() string {
	switch {
	case t.ForEvals():
		return "evals"
	case t.ForRehearsals():
		return "rehearsals"
	}
	return ""
}

// ErrNoCopyRunner: an App Platform target names no Docker host to run its
// copy jobs on.
var ErrNoCopyRunner error = &Unavailable{Reason: "no-copy-runner", Why: "Backups and restore tests need a copy runner: a Docker host that runs the copy commands. An administrator can choose one in the target's settings."}

// Unavailable is why the operations on a deployment's data cannot start
// on its target now: a stable reason code and the sentence a person reads.
// The doors refuse with it and a deployment's protection reports it
// (Protection.Operations), so the card that shows the buttons and the
// refusal a caller gets say the same thing.
type Unavailable struct {
	Reason string
	Why    string
}

func (u *Unavailable) Error() string         { return u.Why }
func (u *Unavailable) FaultKind() fault.Kind { return fault.Conflict }
func (u *Unavailable) FaultCode() string     { return u.Reason }
