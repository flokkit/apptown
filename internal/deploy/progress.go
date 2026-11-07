package deploy

import "context"

type stageReporterKey struct{}

// WithStageReporter attaches the owner's durable progress recorder to this
// execution. Drivers report entry before doing the named work. A recording
// failure stops execution; it must never silently leave progress behind.
func WithStageReporter(ctx context.Context, report func(DeployStage) error) context.Context {
	return context.WithValue(ctx, stageReporterKey{}, report)
}

// ReportStage records a milestone when an owner is watching this execution.
func ReportStage(ctx context.Context, stage DeployStage) error {
	if report, ok := ctx.Value(stageReporterKey{}).(func(DeployStage) error); ok {
		return report(stage)
	}
	return nil
}
