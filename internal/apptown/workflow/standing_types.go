package workflow

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// JanitorChoices records what a person permits AppTown to keep updated.
type JanitorChoices struct {
	Dependencies     string `json:"dependencies"`
	SecurityFixes    bool   `json:"securityFixes"`
	BaseImages       bool   `json:"baseImages"`
	ToolchainPatches bool   `json:"toolchainPatches"`
	Packs            bool   `json:"packs"`
}

func (c JanitorChoices) validate() error {
	if c.Dependencies != "off" && c.Dependencies != "routine" && c.Dependencies != "aggressive" {
		return fault.New(fault.Invalid, "select off, routine or aggressive for dependencies")
	}
	if c.SecurityFixes && c.Dependencies == "off" {
		return fault.New(fault.Invalid, "security fixes require dependency updates")
	}
	if c.ToolchainPatches && !c.SecurityFixes {
		return fault.New(fault.Invalid, "toolchain patches require security fixes")
	}
	return nil
}
func (c JanitorChoices) enabled() bool { return c.Dependencies != "off" || c.BaseImages || c.Packs }

type JanitorChoicesCommand struct {
	Choices          JanitorChoices `json:"choices"`
	ExpectedRevision int64          `json:"expectedRevision"`
}
type StandingRevision struct {
	RuleID   ids.ID         `json:"ruleId"`
	Revision int64          `json:"revision"`
	Enabled  bool           `json:"enabled"`
	Choices  JanitorChoices `json:"choices"`
	Issuer   access.Actor   `json:"issuer"`
	At       time.Time      `json:"at"`
}

// StandingPurpose is the currently selected declaration, never a vote.
type StandingPurpose struct {
	Trigger   string   `json:"-"`
	Workflow  string   `json:"workflow"`
	Version   int64    `json:"version"`
	Step      string   `json:"step"`
	Means     string   `json:"means"`
	Approvers []string `json:"approvers"`
}
type StandingProcedure struct {
	Trigger    string          `json:"trigger"`
	Acceptance StandingPurpose `json:"acceptance"`
	Landing    StandingPurpose `json:"landing"`
}

// standingProcedures resolves declarations and current law under the caller's
// project lock. The workflow owner separately checks approval eligibility.
type standingProcedures interface {
	StandingProcedures(context.Context, database.Queryer, ids.ID, []string) (map[string]StandingProcedureResult, error)
}

type StandingProcedureResult struct {
	Procedure StandingProcedure
	Err       error
}
