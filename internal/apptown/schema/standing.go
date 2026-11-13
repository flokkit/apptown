package schema

// StandingTrigger is the standing work declared by a workflow.
type StandingTrigger struct {
	Queue, Cron, AggressiveCron string
	Publication                 bool
}

// Standing reads the definition's standing trigger in one place.
func (d *Doc) Standing() (StandingTrigger, bool) {
	if s := d.Spec.On.Schedule; s != nil && s.Standing {
		return StandingTrigger{Queue: s.Queue, Cron: s.Cron, AggressiveCron: s.AggressiveCron}, true
	}
	if p := d.Spec.On.Publication; p != nil && p.Standing {
		return StandingTrigger{Queue: p.Queue, Publication: true}, true
	}
	return StandingTrigger{}, false
}

func (s StandingTrigger) CronFor(level string) string {
	if level == "aggressive" && s.AggressiveCron != "" {
		return s.AggressiveCron
	}
	return s.Cron
}

// DisplayTitle is the authored short name, or the workflow's name.
func (m Metadata) DisplayTitle() string {
	if m.Title != "" {
		return m.Title
	}
	return m.Name
}

// RepairRounds counts the repair sessions the procedure declares.
func (d *Doc) RepairRounds() int {
	n := 0
	for _, stage := range d.Spec.Stages {
		for _, step := range stage.Steps {
			if step.Uses == "agent/pi" {
				if count, ok := asInt(step.With["repairRounds"]); ok {
					n += count
				}
			}
		}
	}
	return n
}

// Calls lists the workflows this procedure calls, in declaration order.
func (d *Doc) Calls() []string {
	var out []string
	for _, stage := range d.Spec.Stages {
		for _, step := range append(append([]Step{}, stage.OnEnter...), stage.Steps...) {
			if step.Uses == "workflow/call" {
				if name, ok := step.With["workflow"].(string); ok {
					out = append(out, name)
				}
			}
		}
	}
	return out
}
