package apptown

import (
	"errors"
	"log"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func reportWorkflowDispatch(err error) {
	log.Printf("workflow dispatch: %v", err)
	var panicErr *workflow.DispatchPanic
	if errors.As(err, &panicErr) && selfPanicReporter != nil {
		selfPanicReporter(panicErr.Where, panicErr.Message, panicErr.Stack)
	}
}
