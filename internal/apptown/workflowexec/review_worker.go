package workflowexec

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Review steps on a worker (design/operations/workers.md, "Agent and review
// steps on the worker"). A review's attempt is an agent attempt
// (agent_attempt.go): one workspace, its invocations driven one at a time,
// its commits collected once as its candidate and published as an agent's.
// What differs is data: its request (the brief, no steering, the verdict
// declared as a Fresh result file), its sequence and its verdict
// (reviewStep), and its scope, read in the hub over the whole change.

// hubScope reads a change's files in the hub, from its base (reviewBase)
// to rev.
func hubScope(hub string, source workflow.Source, rev string) scopeReader {
	return func(ctx context.Context) (string, []repositoryedit.ChangedFile, error) {
		base, err := reviewBase(ctx, hub, source, rev)
		if err != nil {
			return "", nil, err
		}
		files, err := repositoryedit.ChangedFilesInHub(ctx, hub, base, rev)
		return base, files, err
	}
}

// hubReviewDiff is the diff the reviewer reads, from the hub: from
// reviewBase (or the default branch) to the attempt's source commit,
// hardened, with no external diff or textconv. One that cannot be read is
// an error: a brief without its diff is not given.
func hubReviewDiff(ctx context.Context, hub string, source workflow.Source, commit string) (string, error) {
	base, err := reviewBase(ctx, hub, source, commit)
	if err != nil {
		return "", fmt.Errorf("reading the change's base for the reviewer: %w", err)
	}
	raw, err := gitexec.HubOutput(ctx, hub, nil, "diff", "--no-ext-diff", "--no-textconv", base+"..."+commit, "--")
	if err != nil {
		return "", fmt.Errorf("reading the change's diff for the reviewer (%s...%s): %w", base, commit, err)
	}
	return boundedDiff(string(raw)), nil
}

// reviewStep is the verdict table: what one review invocation's recorded
// facts give. A verdict counts only from an invocation that exited 0, with
// no model-provider error and its report present and valid. The first
// invocation, "review", may earn one more: a wrap-up when it timed out (its
// stop confirmed; an unconfirmed one never reaches here) or exited 0
// without a report, a fixup when its report is invalid. The wrap-up or
// fixup follows the same rules and never earns another. Otherwise failure
// says why no verdict counts.
func reviewStep(invocation string, res invocationResult) (v reviewVerdict, next, failure string) {
	first := invocation == reviewInvocation
	switch {
	case res.TimedOut && first:
		return v, reviewWrapUpInvocation, ""
	case res.TimedOut:
		return v, "", "the review timed out, and so did its wrap-up; there is no verdict"
	case res.outputErr != nil:
		return v, "", "the review's output could not be read, so whether it completed is unknown: " + res.outputErr.Error()
	case res.ExitCode != 0:
		return v, "", fmt.Sprintf("the review did not complete: its %s exited with code %d, so its report does not count", invocation, res.ExitCode)
	case res.ProviderError != "":
		return v, "", "the model provider refused the request: " + strs.Truncate(res.ProviderError, 300)
	case res.resultsErr != nil:
		return v, "", "the review's report could not be read: " + res.resultsErr.Error()
	}
	f, found := res.results["review"]
	switch {
	case !found:
		return v, "", "the review's report could not be read: the worker returned no result for it"
	case f.State == execution.ResultAbsent && first:
		return v, reviewWrapUpInvocation, ""
	case f.State == execution.ResultAbsent:
		return v, "", "reviewer produced no .apptown/review.json verdict, even after the wrap-up"
	case f.State == execution.ResultRefused:
		return v, "", "the review's report could not be read: the worker refused to read it (" + f.Reason + ")"
	case f.State == execution.ResultTooLarge:
		return v, "", fmt.Sprintf("the review's report could not be read: it is %d bytes, over the %d KiB a report may be", f.Size, maxReviewReport>>10)
	case f.State != execution.ResultPresent:
		return v, "", fmt.Sprintf("the review's report could not be read: its result is %q", f.State)
	}
	if v, ok := parseReviewVerdict(f.Content); ok {
		return v, "", ""
	}
	if first {
		return v, reviewFixUpInvocation, ""
	}
	return v, "", "reviewer verdict unparseable"
}

// reviewJudgement is a review attempt's verdict as judged, before its
// candidate is published.
type reviewJudgement struct {
	verdict reviewVerdict
	failure string
	fields  map[string]any
}

// judgeReview judges the attempt's last invocation by the verdict table,
// then guards a verdict with the scope rules over the whole change: from
// the change's base to the commit the review was given, in the hub. What
// the reviewer itself left (the candidate's own commits) is not the
// change's and is never published, so it is not judged as the change's
// either. A scope that cannot be read is "could not be assessed", never an
// empty scope.
func (a *Executor) judgeReview(ctx context.Context, w *workerAttempt, meta agentAdmission, res invocationResult, hub string, candidate repositoryedit.Candidate) reviewJudgement {
	j := reviewJudgement{fields: map[string]any{
		"model": res.Model, "provider": res.Provider, "modelSlot": meta.ModelSlot, "modelProfile": meta.ModelProfile,
		"piVersion": meta.Image.Labels[dockerlabel.PiVersion], "image": meta.Image.Name,
		"durationMs": a.now().Sub(meta.Began).Milliseconds(), "queuedMs": meta.QueuedMs,
		"reviewedReport": meta.Review.Of,
	}}
	if meta.Invocation != reviewInvocation {
		j.fields["resumed"] = true
	}
	if res.UsageIncomplete != "" {
		j.fields["usageIncomplete"] = res.UsageIncomplete
	}
	j.verdict, _, j.failure = reviewStep(meta.Invocation, res)
	if j.failure == "reviewer verdict unparseable" {
		j.fields["raw"] = strs.Truncate(string(res.results["review"].Content), 1000)
	}
	if j.failure == "" {
		scope := hubScope(hub, w.source, candidate.Source)
		if err := a.guardReviewScope(ctx, w.run, scope, meta.Review.Of, meta.Review.Claim, &j.verdict, j.fields); err != nil {
			j.failure = scopeUnassessed(err)
		}
	}
	return j
}

// evidence is the judgement's evidence with where the review ran and its
// result as kept.
func (j reviewJudgement) evidence(worker, source string, candidate repositoryedit.Candidate) map[string]any {
	j.fields["worker"], j.fields["source"] = worker, source
	if candidate.Ref != "" {
		j.fields["result"] = taskResultEvidence{Ref: candidate.Ref, Commit: candidate.Tip, Refused: candidate.Refused}
	}
	return j.fields
}
