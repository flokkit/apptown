package main

import (
	"context"
	"fmt"
)

// Keep the required database repair ahead of every operation that can resume
// execution or serve writes. Optional/provider recovery belongs inside start.
func recoverThenStart(recover func() error, start func() error) error {
	if err := recover(); err != nil {
		return fmt.Errorf("required startup recovery failed: %w", err)
	}
	return start()
}

// resumeWork resumes what the last process left running, after syncing the
// shipped scanner bindings: a resumed scan reads its scanner's binding when
// it starts, so a scan resumed across an upgrade must find the new one (a
// landing's history scan once ran tree-only that way).
func resumeWork(ctx context.Context, syncScanners, resume func(context.Context)) {
	syncScanners(ctx)
	resume(ctx)
}
