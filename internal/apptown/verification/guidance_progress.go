package verification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

type checkProgressKey struct{}

// Only the candidate's declared checks receive this context. Baseline work
// must not count the same declaration twice. Evaluators run sequentially.
type checkProgress struct {
	total, position int
	now             func() time.Time
	append          func(string)
}

func withCheckProgress(ctx context.Context, total int, now func() time.Time, appendLine func(string)) context.Context {
	return context.WithValue(ctx, checkProgressKey{}, &checkProgress{total: total, now: now, append: appendLine})
}

func startCheck(ctx context.Context, d check.Declared) func(workflow.CheckReceipt) {
	p, _ := ctx.Value(checkProgressKey{}).(*checkProgress)
	if p == nil {
		return func(workflow.CheckReceipt) {}
	}
	p.position++
	position := fmt.Sprintf("check %d", p.position)
	if p.total > 0 {
		position = fmt.Sprintf("%d of %d", p.position, p.total)
	}
	label := fmt.Sprintf("%s · %s (%s)", position, d.Name, d.Namespace)
	start := p.now()
	p.append(label + " · checking")
	return func(r workflow.CheckReceipt) {
		verdict := strings.ReplaceAll(r.Verdict.String(), "-", " ")
		switch r.Verdict {
		case workflow.VerdictPass:
			verdict = "conforms"
		case workflow.VerdictFail:
			verdict = "does not conform"
		}
		if r.AgentEvidence != nil && r.AgentEvidence.Reused {
			verdict = "reused"
		}
		p.append(fmt.Sprintf("%s · %s · %.1fs", label, verdict, p.now().Sub(start).Seconds()))
	}
}
