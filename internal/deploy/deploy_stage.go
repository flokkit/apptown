package deploy

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

// DeployStage names milestones reported by a release's execution. Providers
// report entry to real work; elapsed time and log wording never select a stage.
type DeployStage uint8

const (
	Preparing DeployStage = iota + 1
	BackingUp
	Building
	Starting
	Checking
	Live
)

var deployStages = enum.Set[DeployStage]{Names: []string{"preparing", "backing-up", "building", "starting", "checking", "live"}, Invalid: "invalid-deploy-stage", Err: errors.New("invalid deploy stage")}

func (s DeployStage) String() string                 { return deployStages.String(s) }
func ParseDeployStage(s string) (DeployStage, error) { return deployStages.Parse(s) }
func (s DeployStage) MarshalJSON() ([]byte, error)   { return deployStages.JSON(s) }
func (s *DeployStage) UnmarshalJSON(b []byte) error  { return deployStages.ReadJSON(s, b) }

func (s DeployStage) Value() (driver.Value, error) { return deployStages.Value(s) }
