import './Steps.css'
import { Status } from '../../Status'
import { Link } from 'react-router-dom'
import { Run, Step, currentUser, post, type ApprovalConfig } from '../../api'
import { QuietPrompt } from '../../QuietConfirm'
import { AddsSection, DecisionForm } from '../../DecisionForm'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { useAction, useGet } from '../../data'
import { RunFindings } from './Findings'
import type { StepProgress } from '../../wire.gen'
import { checkMethod, checkPurpose, stepTitle, testsNote } from './Evidence'
import { decisionStatus, checksRunning } from './next'
import { closingVia, voterWords } from './through'
import { fmtStamp } from '../../format'
import { useTechnicalDetail } from './TechnicalDetail'
import { Screenshots, sameCommitWords } from '../../Screenshots'

// ---- step cards ------------------------------------------------------------

export function StepCard({ step, onChange, hideApproveForm, landingMeans, head, awaitingDecision, failedCheck, waitingChecks, evidenceOnly = false }: { evidenceOnly?: boolean; waitingChecks?: boolean; awaitingDecision?: boolean; step: Step; onChange: () => void; hideApproveForm?: boolean; landingMeans?: string[]; head?: string; failedCheck?: string }) {
  const ev = step.evidence
  const [technical] = useTechnicalDetail()
  const statusLabel = decisionStatus(step, awaitingDecision)
  // A step's own id and its run's are always sent; null in the wire type is
  // for an absent reference.
  const stepId = step.id!
  const runId = step.runId!
  const overridden = step.overrideBy !== ''
  const took = ev.durationMs != null ? `${(ev.durationMs / 1000).toFixed(1)}s` : ''
  // Skips and unknown counts stay visible beside the test result.
  const tests = step.type === 'tests' && (step.status === 'pass' || step.status === 'fail') && !overridden
    ? step.evidence.command ? testsNote(step.evidence) : { note: 'no tests found', plain: false }
    : null

  return (
    // Every step has the same sides, so states and names line up.
    <div id={`step-${step.id}`} className="card step-card step-card-inset">
      {!evidenceOnly && <div className="row step-head">
        {/* The tests' note is said beside the chip, so the chip carries no
            help that would repeat it. */}
        <Status word={statusLabel === 'passed' && tests && !tests.plain && !(step.type === 'tests' && step.evidence.testCounts) ? 'unknown' : statusLabel} />
        {statusLabel === 'failed' && tests && !(step.type === 'tests' && step.evidence.testCounts) && <Status word="unknown" />}
        <strong>{stepTitle(step)}</strong>
        <span className="muted small">{took}{tests?.plain && ` · ${tests.note}`}</span>
        {tests && !tests.plain && <span className="muted small">{tests.note}</span>}
        <StepActions step={step} onChange={onChange} />
      </div>}

      {evidenceOnly && step.type === 'approval' && (() => {
        const c = step.config as ApprovalConfig
        return <dl className="ch-check-about">
          <dt>Who decides</dt><dd>{c.approvers?.length ? c.approvers.join(', ') : 'anyone allowed to approve changes in this project'}{(c.quorum ?? 1) > 1 && ` · ${c.quorum} of them must accept`}</dd>
          {c.means && <><dt>Accepting means</dt><dd>“{c.means}”</dd></>}
          {c.instructions && <><dt>Asked to check</dt><dd>{c.instructions}</dd></>}
        </dl>
      })()}
      {evidenceOnly && step.isCheck && <dl className="ch-check-about">
        <dt>What it checks</dt><dd>{checkPurpose(step) ?? <span className="muted">— this workflow’s own check; it has no description yet</span>}</dd>
        {checkMethod(step) && <><dt>How</dt><dd><code>{checkMethod(step)!.replace(/^Ran /, '')}</code></dd></>}
      </dl>}
      {overridden && (
        <div className="muted small">Exception by {step.overrideBy}: “{step.overrideReason}”</div>
      )}

      {/* Names, times and ids are for people wearing the goggles. */}
      {technical && <section className="small muted mt-2" >
        <p>{step.name} · {step.type}</p>{(step.startedAt || step.finishedAt) && <p>{[step.startedAt && `Started ${fmtStamp(step.startedAt)}`, step.finishedAt && `Finished ${fmtStamp(step.finishedAt)}`].filter(Boolean).join(' · ')}</p>}
        <p>Step: {step.id} · Run: {step.runId}</p>
      </section>}
      {step.type === 'policy/current' && step.status === 'needs_human' && (
        <div className="mt-2">
          <div>{step.evidence.reason ?? 'The rules changed while this landing waited.'}</div>
          {(step.evidence.violations ?? []).map((v, i) => (
            <div key={i} className="muted small mt-1" >• {v.policy}: {v.reading}</div>
          ))}

        </div>
      )}
      {/* An approval is a separate fact from a check: while a check has
          failed, approving would not land the change, so the form waits and
          the way past a failed check (accepting it anyway) is named instead. */}
      {step.type === 'approval' && step.status === 'pending' && waitingChecks && <p>Checks are running; you can accept when they finish.</p>}
      {step.type === 'approval' && step.status === 'pending' && !hideApproveForm && failedCheck && (
        <p className="muted small">Approval opens once the checks pass. “{failedCheck}” failed: fix it, or accept it anyway on its step.</p>
      )}
      {step.type === 'approval' && step.status === 'pending' && !hideApproveForm && !failedCheck && !waitingChecks && (
        <DecisionForm stepId={stepId} means={step.config.means} alsoMeans={landingMeans} instructions={step.config.instructions} onDone={onChange} />
      )}
      {step.type === 'approval' && step.evidence.withdrawal && (
        <div className="muted small">acceptance withdrawn by {step.evidence.withdrawal.by}: “{step.evidence.withdrawal.reason}”</div>
      )}
      {step.type === 'approval' && step.status === 'pending' && step.evidence.lastRejection && (
        <div className="muted small">previously rejected by {step.evidence.lastRejection.approver}: “{step.evidence.lastRejection.comment}”</div>
      )}
      {step.type === 'approval' && step.status === 'pass' && step.evidence.evalAuthorised && (
        <div className="muted small">eval-authorised, not a person's approval: {step.evidence.evalAuthorised}</div>
      )}
      {step.type === 'approval' && (step.status === 'pass' || step.status === 'fail') && step.evidence.approver != null && (
        <div className="muted small">{step.evidence.decision === 'approve' ? 'approved' : 'rejected'} by {voterWords(step.evidence.approver, closingVia(step.evidence))}{step.evidence.comment ? `: “${step.evidence.comment}”` : ''}</div>
      )}
      {/* What each approving vote was shown the commit adds, as it was
          recorded with the vote: the saved list, not today's question. */}
      {step.type === 'approval' && (step.evidence.decisions ?? []).filter((d) => d.adds).map((d) => (
        <div key={`adds-${d.approver}-${d.at ?? ''}`} className="saved-adds">
          <div className="muted small">shown to {voterWords(d.approver, d.via)} when {d.decision === 'approve' ? 'approving' : 'deciding'}:</div>
          <AddsSection adds={d.adds!} />
        </div>
      ))}
      {step.type === 'approval' && step.evidence.decisions && ((step.evidence.quorum ?? 0) > 1 || step.evidence.decisions.length > 1) && (
        <div className="muted small">
          {step.status === 'pending' && <>{step.evidence.approvals ?? 0} of {step.evidence.quorum} OKs so far · </>}
          {step.evidence.decisions.map((d) => `${voterWords(d.approver, d.via)} ${d.decision === 'approve' ? '✓' : '✗'}`).join(', ')}
        </div>
      )}

      {step.type === 'agent/review' && (
        <div className="step-note">
          {step.status === 'running' && <LiveFeed stepId={stepId} />}
          {step.evidence.verdict != null && (
            <div>
              <div className="muted small">
                verdict: <strong>{step.evidence.verdict}</strong> · {step.evidence.provider}/{step.evidence.model} · {step.evidence.durationMs}ms
                {step.evidence.reviewedReport ? <> · audited “{step.evidence.reviewedReport}” claim vs diff</> : null}
              </div>
              {step.evidence.summary != null && <div className="small mt-1" >{step.evidence.summary}</div>}
              {step.evidence.reviewerSummary && <div className="small muted mt-1" >The reviewer itself passed it: {step.evidence.reviewerSummary}</div>}
              {(step.evidence.findings?.length ?? 0) > 0 && (
                <ul className="small step-list">
                  {step.evidence.findings!.map((f, i) => <li key={i}>{String(f)}</li>)}
                </ul>
              )}
            </div>
          )}
          {ev.reason != null && step.status !== 'pass' && <div className="error">{ev.reason}</div>}
        </div>
      )}

      {step.type === 'agent/pi' && step.status === 'running' && <LiveFeed stepId={stepId} />}
      {['build', 'scan', 'tests'].includes(step.type) && step.status === 'running' && (
        <LiveFeed stepId={stepId} label={step.type === 'build' ? 'building' : step.type === 'tests' ? 'running tests' : 'scanning'} />
      )}
      {(step.type === 'agent/pi' || step.type === 'agent/review') && step.status !== 'running' && step.status !== 'pending' && (
        <FinishedFeed stepId={stepId} omitEmpty={evidenceOnly} />
      )}
      {step.type === 'agent/pi' && step.evidence.output != null && (
        <section className="mt-2">
          <h4>
            {step.evidence.provider}/{step.evidence.model} · {step.evidence.durationMs}ms
            {step.evidence.guidanceHash ? <> · guidance sha256:{step.evidence.guidanceHash.slice(0, 12)}…</> : null}
            {step.evidence.skills && Object.keys(step.evidence.skills).length > 0 ? <> · skills: {Object.keys(step.evidence.skills).join(', ')}</> : null}
          </h4>
          <pre className="log step-log">{step.evidence.output}</pre>
        </section>
      )}
      {step.type === 'preview' && step.evidence.url != null && (
        <div className="muted small step-note">
          <a href={step.evidence.url} target="_blank" rel="noreferrer">{step.evidence.url}</a>
          {step.evidence.commit && <> · built from commit <code>{step.evidence.commit.slice(0, 7)}</code>{head ? <> ({sameCommitWords(step.evidence.commit, head).text})</> : null}</>}
        </div>
      )}
      {step.type === 'preview' && step.evidence.screenshots && (
        <Screenshots shots={step.evidence.screenshots} head={head} at={step.finishedAt ?? undefined} />
      )}
      {(step.type === 'agent/pi' || step.type === 'preview' || step.type === 'build') && ev.reason != null && (
        <div className="error">{ev.reason}{ev.logTail ? <pre className="log">{ev.logTail}</pre> : null}</div>
      )}

      {step.type === 'tests' && step.evidence.inputs && (
        <section className="mt-2">
          <h4>Test inputs</h4>
          <pre className="log">{JSON.stringify(step.evidence.inputs, null, 2)}</pre>
        </section>
      )}
      {step.type === 'tests' && (step.evidence.skippedTests?.length ?? 0) > 0 && (
        <div className="muted small step-note">
          skipped: {step.evidence.skippedTests!.join(', ')}
          {(step.evidence.testCounts?.skipped ?? 0) > step.evidence.skippedTests!.length && ', …'}
        </div>
      )}
      {step.type === 'tests' && !step.evidence.testCounts && step.evidence.testReport && (
        <div className="muted small step-note">{step.evidence.testReport}</div>
      )}
      {(step.type === 'script' || step.type === 'tests') && (step.evidence.log ?? step.evidence.logTail) != null && (
        <section className="mt-2">
          {/* What the check said comes first; how it ran is a grey line for the goggles. */}
          <pre className="log">{step.evidence.log ?? step.evidence.logTail}</pre>
          {technical && <p className="muted small step-summary">
            {[step.evidence.exitCode != null && `exit ${step.evidence.exitCode}`, step.evidence.durationMs != null && `${step.evidence.durationMs}ms`].filter(Boolean).join(' · ')}
          </p>}
        </section>
      )}

      {step.type === 'scan' && step.evidence.binding != null && (
        <div className="step-note">
          <div className="muted small">
            {step.evidence.binding} v{step.evidence.bindingVersion} ({step.evidence.image}) · exit {step.evidence.exitCode} ·
            {' '}{step.evidence.durationMs}ms · config sha256:{step.evidence.configHash}
            {step.evidence.note ? <> · {step.evidence.note}</> : null}
            {step.evidence.findings != null && (
              <> · {step.evidence.findings.actionable} actionable / {step.evidence.findings.total} findings
                {step.evidence.findings.dispositioned > 0 && <> ({step.evidence.findings.dispositioned} suppressed)</>}</>
            )}
          </div>
          {step.evidence.findings != null && (
            <section className="mt-1">
              <h4>Findings</h4>
              <RunFindings runId={runId} onChange={onChange} />
            </section>
          )}
          {ev.reason != null && step.status !== 'pass' && <div className="error">{ev.reason}</div>}
        </div>
      )}

      {step.type === 'guidance/conformance' && step.checkReading && (
        <div className="step-note">
          {step.checkReading.unreadable && (
            <div className="row"><Status word="unknown" /> <span className="muted small">{step.checkReading.unreadable}</span></div>
          )}
          {(step.checkReading.checks ?? []).map((c, i) => (
            <div key={i} className="row step-detail">
              <Status word={c.statusLabel} />
              {c.undecided && <span className="badge" title="Not known whether this change introduced it; it does not block">undecided</span>}
              {c.preexisting && <span className="badge" title="Main already fails this check; it does not block">already on main</span>}
              <span className="small">{c.check}</span>
              <span className="muted small">
                {c.block} v{c.version} · {c.evaluator}
                {c.gates === false && ' · advisory (never blocks)'}
                {c.evidence ? ` · ${c.evidence}` : ''}
              </span>
              {c.note && <span className="muted small">{c.note}</span>}
              {c.verdict === 'not-applicable' && c.detail && <span className="muted small">{c.detail}</span>}
            </div>
          ))}
          {ev.reason != null && step.status !== 'pass' && <div className="error">{ev.reason}</div>}
          {step.evidence.findingsSummary != null && step.evidence.findingsSummary.total > 0 && (
            <section className="mt-1">
              <h4>Findings</h4>
              <RunFindings runId={runId} onChange={onChange} />
            </section>
          )}
        </div>
      )}

      {step.type === 'workflow/call' && step.evidence.childRunId != null && (
        <ChildRunSteps
          childId={step.evidence.childRunId}
          childNumber={step.evidence.childRunNumber}
          workflow={`${step.evidence.workflow} v${step.evidence.workflowVersion ?? '?'}`}
          live={step.status === 'running'}
          onChange={onChange}
        />
      )}
      {step.type === 'workflow/call' && ev.reason != null && (
        <div className="error">{ev.reason}</div>
      )}

      {(step.type === 'adr/require' || step.type === 'artifact/require' || step.type === 'scm/merged') && (
        <div className="muted small step-note">
          {ev.reason != null && <span>{ev.reason}</span>}
          {step.evidence.adrs?.map((m) => (
            <span key={m.id}> satisfied by <Link to={`/adrs/${m.id}`}>ADR-{String(m.number).padStart(3, '0')} {m.title}</Link></span>
          ))}
          {step.evidence.artifactId != null && <span> satisfied by artifact “{step.evidence.name || step.evidence.kind}”</span>}
        </div>
      )}
    </div>
  )
}

export function StepActions({ step, onChange }: { step: Step; onChange: () => void }) {
  const { run: act, err, busy } = useAction(onChange)
  const overridden = !!step.overrideBy
  return <>
        {step.type === 'policy/current' && step.status === 'needs_human' && <button className="link" disabled={busy} onClick={() => act(() => post(`/runs/${step.runId}/restart-landing`, {}))}>Retry landing</button>}
        <span className="step-acts" aria-busy={busy}>
          {['script', 'agent/pi', 'preview', 'build', 'agent/review', 'scan', 'guidance/conformance'].includes(step.type) && step.status !== 'running' && (
            <button className="link" onClick={() => act(() => post(`/steps/${step.id}/run`))}>Re-run</button>
          )}
          {(step.type === 'artifact/require' || step.type === 'adr/require' || step.type === 'scm/merged') && step.status !== 'pass' && (
            <button className="link" onClick={() => act(() => post(`/steps/${step.id}/run`))}>Re-check</button>
          )}
          {/* A failed check may be overridden with a reason; an approval is a person's decision and never is. */}
          {!overridden && step.type !== 'approval' && step.status !== 'pass' && step.status !== 'running' && step.status !== 'not_applicable' && (
            <QuietPrompt label="Override" className="link danger" yes="Override"
              ask="Reason for the override (recorded):"
              fields={[{ key: 'reason', placeholder: 'justification', required: true }]}
              onYes={(v) => act(() => post(`/steps/${step.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
          )}
        </span>
    {err && <span className="error small">{err}</span>}
  </>
}

// A called workflow's child run, rendered inline inside its call step: the
// child's checks with actionable approvals — approvals inside the callee
// bubble up to the parent's page (composition.md §2).
export function ChildRunSteps({ childId, childNumber, workflow, live, onChange }: {
  // childNumber is from the call's evidence; evidence written before runs
  // were numbered lacks it, and the child's trace names it instead.
  childId: string; childNumber?: number; workflow: string; live: boolean; onChange: () => void
}) {
  const read = useGet<{ run: Run | null; steps: Step[] }>(`/runs/${childId}/trace`, { poll: live ? 3000 : 0 })
  const trace = read.data
  const steps = trace?.steps ?? []
  const failed = steps.some(s => s.stage === trace?.run?.currentStage && s.isCheck && ['fail', 'error', 'needs_human'].includes(s.status) && !s.overrideBy)
  const { run: act, err } = useAction(onChange)

  return (
    <div className="ch-child-steps mt-2 step-children">
      <div className="muted small">↳ {workflow} · child run{childNumber ? ` ${childNumber}` : trace?.run ? ` ${trace.run.number}` : ''}</div>
      <ReadStatus q={read} what="the called workflow" /><StaleNote q={read} />
      {steps.map((s) => {
        const url = s.type === 'preview' ? s.evidence?.url : undefined
        const reason = s.evidence?.reason
        return (
          <div key={s.id} className="row mt-1 step-actions">
            <Status word={s.statusLabel} />
            <span className="small">{s.name}</span>
            <span className="muted small">{s.type}</span>
            {url != null && <a href={url} target="_blank" rel="noreferrer" className="small">{url}</a>}
            {reason != null && s.status !== 'pass' && <span className="muted small">{reason.slice(0, 90)}</span>}
            {s.checkReading?.unreadable && <Status word="unknown" help={`${s.checkReading.unreadable} Re-run the check to replace the unreadable evidence.`} />}
            <span className="grow" />
            {s.type === 'approval' && s.status === 'pending' && checksRunning(steps, trace?.run?.currentStage) && <p>Checks are running; you can accept when they finish.</p>}
            {s.type === 'approval' && s.status === 'pending' && !failed && live && read.current && trace?.run?.status === 'running' && s.stage === trace.run.currentStage && !checksRunning(steps, trace.run.currentStage) && (
              <DecisionForm stepId={s.id!} means={s.config.means} instructions={s.config.instructions} onDone={onChange} approveLabel="Accept" rejectLabel="Send back with a note" />
            )}
            {['script', 'agent/pi', 'preview', 'build', 'workflow/call', 'scan', 'agent/review'].includes(s.type) && ['fail', 'error'].includes(s.status) && (
              <button className="subtle" onClick={() => act(() => post(`/steps/${s.id}/run`))}>Re-run</button>
            )}
            {!s.overrideBy && ['fail', 'error', 'needs_human'].includes(s.status) && (
              <QuietPrompt label="Override" className="subtle danger" yes="Override"
                ask="Reason for the override (recorded):"
                fields={[{ key: 'reason', placeholder: 'justification', required: true }]}
                onYes={(v) => act(() => post(`/steps/${s.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
            )}
          </div>
        )
      })}
      {steps.some((s) => s.type === 'scan') && (
        <section className="step-note">
          <h4>Findings</h4>
          <RunFindings runId={childId} onChange={onChange} />
        </section>
      )}
      {err && <div className="error">{err}</div>}
    </div>
  )
}

export function LiveFeed({ stepId, label }: { stepId: string; label?: string }) {
  // The server keeps a bounded ring of recent lines; asking from 0 each
  // time gets that ring, and the last 200 of it is what fits on screen.
  const { data } = useGet<{ running: boolean; lines: string[]; total: number; elapsedMs?: number }>(
    `/steps/${stepId}/live?from=0`, { poll: 2000 })
  const lines = (data?.lines ?? []).slice(-200)
  const total = data?.total ?? 0
  const elapsed = data?.elapsedMs ?? 0

  return (
    <section className="mt-2">
      <h4>Agent activity</h4>
      <div className="muted small">
        {label ?? 'agent working'}… {Math.round(elapsed / 1000)}s{total > 0 ? ` · ${total} line${total === 1 ? '' : 's'}` : ''}
      </div>
      {lines.length > 0 && (
        <pre className="log step-live-log"  ref={(el) => { if (el) el.scrollTop = el.scrollHeight }}>
          {lines.join('\n')}
        </pre>
      )}
    </section>
  )
}

// FinishedFeed: the persisted tail of a finished agent step's activity —
// what it printed, readable after the fact (post-mortems, timeouts).
export function FinishedFeed({ stepId, omitEmpty = false }: { stepId: string; omitEmpty?: boolean }) {
  const { data, err } = useGet<StepProgress>(`/steps/${stepId}/live?from=0`)
  const lines = data ? data.lines ?? [] : null
  if (omitEmpty && lines?.length === 0 && !err) return null
  return (
    <section className="step-note">
      <h4>Terminal</h4>
      {err ? <div className="error small">The activity could not be read: {err}</div>
        : lines === null ? <div className="muted small">…</div>
        : lines.length === 0 ? <div className="muted small">no terminal output recorded</div>
        : <pre className="small step-finished-log">{lines.join('\n')}</pre>}
    </section>
  )
}
