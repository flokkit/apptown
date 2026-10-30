import { Status, STATUS_LIGHT } from '../../Status'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Run, Step, currentUser, post } from '../../api'
import { QuietPrompt } from '../../QuietConfirm'
import { AddsSection, DecisionForm } from '../../DecisionForm'
import { useAction, useGet } from '../../data'
import { RunFindings } from './Findings'
import type { StepProgress } from '../../wire.gen'
import { testsNote } from './Evidence'
import { closingVia, voterWords } from './through'
import { Screenshots, sameCommitWords } from '../../Screenshots'

// ---- step cards ------------------------------------------------------------

export function StepCard({ step, onChange, hideApproveForm, expanded, landingMeans, head }: { step: Step; onChange: () => void; hideApproveForm?: boolean; expanded?: boolean; landingMeans?: string[]; head?: string }) {
  const ev = step.evidence
  // A step's own id and its run's are always sent; null in the wire type is
  // for an absent reference.
  const stepId = step.id!
  const runId = step.runId!
  const { run: act, err } = useAction(onChange)

  const overridden = step.overrideBy !== ''
  // A check that passed is one line — chip, name, how long — until you ask
  // for more. Only what failed, is running or needs a person opens itself.
  const quiet = (step.status === 'pass' || overridden) && step.type !== 'workflow/call' && !step.checkReading?.unreadable
  const [more, setMore] = useState(false)
  useEffect(() => { if (expanded) setMore(true) }, [expanded])
  const showBody = !quiet || more
  const took = ev.durationMs != null ? `${(ev.durationMs / 1000).toFixed(1)}s` : ''
  // What the tests' report said, beside the duration, so a folded passing
  // card still shows skipped tests or counts not known.
  const tests = step.type === 'tests' && (step.status === 'pass' || step.status === 'fail') && !overridden
    ? step.evidence.command ? testsNote(step.evidence) : { note: 'no tests found', plain: false }
    : null

  return (
    <div id={`step-${step.id}`} className="card step-card" style={quiet && !more ? { padding: '8px 12px' } : undefined}>
      <div className="row step-head">
        <Status word={step.statusLabel} tone={STATUS_LIGHT[step.statusLabel]} />
        <strong>{step.name}</strong>
        <span className="muted small">{step.type}{took && ` · ${took}`}{tests?.plain && ` · ${tests.note}`}</span>
        {tests && !tests.plain && <span className="chip stale small">{tests.note}</span>}
        <span className="step-acts">
          {quiet && (
            <button className="subtle" onClick={() => setMore(!more)}>{more ? 'less' : 'details'}</button>
          )}
          {['script', 'agent/pi', 'preview', 'deploy', 'agent/review', 'scan', 'guidance/conformance'].includes(step.type) && step.status !== 'running' && (
            <button className="subtle" onClick={() => act(() => post(`/steps/${step.id}/run`))}>Re-run</button>
          )}
          {(step.type === 'artifact/require' || step.type === 'adr/require' || step.type === 'scm/merged') && step.status !== 'pass' && (
            <button className="subtle" onClick={() => act(() => post(`/steps/${step.id}/run`))}>Re-check</button>
          )}
          {!overridden && step.status !== 'pass' && step.status !== 'running' && (
            <QuietPrompt label="Override" className="subtle danger" yes="Override"
              ask="Reason for the override (recorded):"
              fields={[{ key: 'reason', placeholder: 'justification', required: true }]}
              onYes={(v) => act(() => post(`/steps/${step.id}/override`, { user: currentUser(), reason: v.reason.trim() }))} />
          )}
        </span>
      </div>

      {overridden && (
        <div className="muted small">Exception by {step.overrideBy}: “{step.overrideReason}”</div>
      )}

      {showBody && <>
      {step.type === 'policy/current' && step.status === 'needs_human' && (
        <div style={{ marginTop: 8 }}>
          <div>{step.evidence.reason ?? 'The rules changed while this landing waited.'}</div>
          {(step.evidence.violations ?? []).map((v, i) => (
            <div key={i} className="muted small" style={{ marginTop: 4 }}>• {v.policy}: {v.reading}</div>
          ))}
          <div className="row" style={{ marginTop: 10 }}>
            <button className="primary" onClick={() => act(async () => {
              await post(`/runs/${step.runId}/restart-landing`, {})
            })}>Restart landing</button>
            <span className="muted small">lands this change again under the current rules</span>
          </div>
        </div>
      )}
      {step.type === 'approval' && step.status === 'pending' && !hideApproveForm && (
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
        <div style={{ marginTop: 6 }}>
          {step.status === 'running' && <LiveFeed stepId={stepId} />}
          {step.evidence.verdict != null && (
            <div>
              <div className="muted small">
                verdict: <strong>{step.evidence.verdict}</strong> · {step.evidence.provider}/{step.evidence.model} · {step.evidence.durationMs}ms
                {step.evidence.reviewedReport ? <> · audited “{step.evidence.reviewedReport}” claim vs diff</> : null}
              </div>
              {step.evidence.summary != null && <div className="small" style={{ marginTop: 4 }}>{step.evidence.summary}</div>}
              {step.evidence.reviewerSummary && <div className="small muted" style={{ marginTop: 4 }}>The reviewer itself passed it: {step.evidence.reviewerSummary}</div>}
              {(step.evidence.findings?.length ?? 0) > 0 && (
                <ul className="small" style={{ margin: '4px 0 0 18px' }}>
                  {step.evidence.findings!.map((f, i) => <li key={i}>{String(f)}</li>)}
                </ul>
              )}
            </div>
          )}
          {ev.reason != null && step.status !== 'pass' && <div className="error">{ev.reason}</div>}
        </div>
      )}

      {step.type === 'agent/pi' && step.status === 'running' && <LiveFeed stepId={stepId} />}
      {['deploy', 'scan', 'tests'].includes(step.type) && step.status === 'running' && (
        <LiveFeed stepId={stepId} label={step.type === 'deploy' ? 'deploying' : step.type === 'tests' ? 'running tests' : 'scanning'} />
      )}
      {(step.type === 'agent/pi' || step.type === 'agent/review') && step.status !== 'running' && step.status !== 'pending' && (
        <FinishedFeed stepId={stepId} />
      )}
      {step.type === 'agent/pi' && step.evidence.output != null && (
        <details style={{ marginTop: 8 }}>
          <summary>
            {step.evidence.provider}/{step.evidence.model} · {step.evidence.durationMs}ms
            {step.evidence.guidanceHash ? <> · guidance sha256:{step.evidence.guidanceHash.slice(0, 12)}…</> : null}
            {step.evidence.skills && Object.keys(step.evidence.skills).length > 0 ? <> · skills: {Object.keys(step.evidence.skills).join(', ')}</> : null}
          </summary>
          <pre className="log" style={{ maxHeight: 400 }}>{step.evidence.output}</pre>
        </details>
      )}
      {(step.type === 'preview' || step.type === 'deploy') && step.evidence.url != null && (
        <div className="muted small" style={{ marginTop: 6 }}>
          <a href={step.evidence.url} target="_blank" rel="noreferrer">{step.evidence.url}</a>
          {step.evidence.commit && <> · built from commit <code>{step.evidence.commit.slice(0, 7)}</code>{head ? <> ({sameCommitWords(step.evidence.commit, head).text})</> : null}</>}
        </div>
      )}
      {step.type === 'preview' && step.evidence.screenshots && (
        <Screenshots shots={step.evidence.screenshots} head={head} at={step.finishedAt ?? undefined} />
      )}
      {(step.type === 'agent/pi' || step.type === 'preview' || step.type === 'deploy') && ev.reason != null && (
        <div className="error">{ev.reason}{ev.logTail ? <pre className="log">{ev.logTail}</pre> : null}</div>
      )}

      {step.type === 'tests' && (step.evidence.skippedTests?.length ?? 0) > 0 && (
        <div className="muted small" style={{ marginTop: 6 }}>
          skipped: {step.evidence.skippedTests!.join(', ')}
          {(step.evidence.testCounts?.skipped ?? 0) > step.evidence.skippedTests!.length && ', …'}
        </div>
      )}
      {step.type === 'tests' && !step.evidence.testCounts && step.evidence.testReport && (
        <div className="muted small" style={{ marginTop: 6 }}>{step.evidence.testReport}</div>
      )}
      {(step.type === 'script' || step.type === 'tests') && (step.evidence.log ?? step.evidence.logTail) != null && (
        <details open={expanded || undefined} style={{ marginTop: 8 }}>
          <summary>
            Recorded output
            {step.evidence.exitCode != null && <> · exit {step.evidence.exitCode}</>}
            {step.evidence.durationMs != null && <> · {step.evidence.durationMs}ms</>}
            {step.evidence.command && <> · <code>{step.evidence.command}</code></>}
          </summary>
          <pre className="log">{step.evidence.log ?? step.evidence.logTail}</pre>
        </details>
      )}

      {step.type === 'scan' && step.evidence.binding != null && (
        <div style={{ marginTop: 6 }}>
          <div className="muted small">
            {step.evidence.binding} v{step.evidence.bindingVersion} ({step.evidence.image}) · exit {step.evidence.exitCode} ·
            {' '}{step.evidence.durationMs}ms · config sha256:{step.evidence.configHash}
            {step.evidence.note ? <> · {step.evidence.note}</> : null}
            {step.evidence.findings != null && (
              <> · {step.evidence.findings.actionable} actionable / {step.evidence.findings.total} findings
                {step.evidence.findings.dispositioned > 0 && <> ({step.evidence.findings.dispositioned} suppressed)</>}</>
            )}
          </div>
          {step.evidence.findings != null && step.evidence.findings.total > 0 && (
            <details style={{ marginTop: 4 }} open={step.status === 'fail'}>
              <summary>Findings</summary>
              <RunFindings runId={runId} onChange={onChange} />
            </details>
          )}
          {ev.reason != null && step.status !== 'pass' && <div className="error">{ev.reason}</div>}
        </div>
      )}

      {step.type === 'guidance/conformance' && step.checkReading && (
        <div style={{ marginTop: 6 }}>
          {step.checkReading.unreadable && (
            <div className="row"><Status word="unknown" tone={STATUS_LIGHT.unknown} /> <span className="muted small">{step.checkReading.unreadable}</span></div>
          )}
          {(step.checkReading.checks ?? []).map((c, i) => (
            <div key={i} className="row" style={{ marginTop: 2 }}>
              <Status word={c.statusLabel} tone={STATUS_LIGHT[c.statusLabel]} />
              {c.undecided && <span className="chip pending" title="Not known whether this change introduced it; it does not block">undecided</span>}
              {c.preexisting && <span className="chip" title="Main already fails this check; it does not block">already on main</span>}
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
            <details style={{ marginTop: 4 }} open={step.status === 'fail'}>
              <summary>Findings</summary>
              <RunFindings runId={runId} onChange={onChange} />
            </details>
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
        <div className="muted small" style={{ marginTop: 6 }}>
          {ev.reason != null && <span>{ev.reason}</span>}
          {step.evidence.adrs?.map((m) => (
            <span key={m.id}> satisfied by <Link to={`/adrs/${m.id}`}>ADR-{String(m.number).padStart(3, '0')} {m.title}</Link></span>
          ))}
          {step.evidence.artifactId != null && <span> satisfied by artifact “{step.evidence.name || step.evidence.kind}”</span>}
        </div>
      )}
      </>}
      {err && <div className="error">{err}</div>}
    </div>
  )
}

// A called workflow's child run, rendered inline inside its call step: the
// child's checks with actionable approvals — approvals inside the callee
// bubble up to the parent's page (composition.md §2).
export function ChildRunSteps({ childId, childNumber, workflow, live, onChange }: {
  // childNumber is from the call's evidence; evidence written before runs
  // were numbered lacks it, and the child's trace names it instead.
  childId: string; childNumber?: number; workflow: string; live: boolean; onChange: () => void
}) {
  const { data: trace } = useGet<{ run: Run | null; steps: Step[] }>(`/runs/${childId}/trace`, { poll: live ? 3000 : 0 })
  const steps = trace?.steps ?? []
  const { run: act, err } = useAction(onChange)

  return (
    <div style={{ marginTop: 8, borderLeft: '2px solid var(--border)', paddingLeft: 12 }}>
      <div className="muted small">↳ {workflow} · child run{childNumber ? ` ${childNumber}` : trace?.run ? ` ${trace.run.number}` : ''}</div>
      {steps.map((s) => {
        const url = (s.type === 'preview' || s.type === 'deploy') ? s.evidence?.url : undefined
        const reason = s.evidence?.reason
        return (
          <div key={s.id} className="row" style={{ marginTop: 4 }}>
            <Status word={s.statusLabel} tone={STATUS_LIGHT[s.statusLabel]} />
            <span className="small">{s.name}</span>
            <span className="muted small">{s.type}</span>
            {url != null && <a href={url} target="_blank" rel="noreferrer" className="small">{url}</a>}
            {reason != null && s.status !== 'pass' && <span className="muted small">{reason.slice(0, 90)}</span>}
            {s.checkReading?.unreadable && <span className="chip stale" title={s.checkReading.unreadable}>evidence unreadable</span>}
            <span style={{ flex: 1 }} />
            {s.type === 'approval' && s.status === 'pending' && (
              <DecisionForm compact stepId={s.id!} means={s.config.means} instructions={s.config.instructions} onDone={onChange} />
            )}
            {['script', 'agent/pi', 'preview', 'deploy', 'workflow/call', 'scan', 'agent/review'].includes(s.type) && ['fail', 'error'].includes(s.status) && (
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
        <details style={{ marginTop: 6 }} open={steps.some((s) => s.type === 'scan' && s.status === 'fail')}>
          <summary>Findings</summary>
          <RunFindings runId={childId} onChange={onChange} />
        </details>
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
    <div style={{ marginTop: 8 }}>
      <div className="muted small">
        {label ?? 'agent working'}… {Math.round(elapsed / 1000)}s{total > 0 ? ` · ${total} line${total === 1 ? '' : 's'}` : ''}
      </div>
      {lines.length > 0 && (
        <pre className="log" style={{ maxHeight: 260 }} ref={(el) => { if (el) el.scrollTop = el.scrollHeight }}>
          {lines.join('\n')}
        </pre>
      )}
    </div>
  )
}

// FinishedFeed: the persisted tail of a finished agent step's activity —
// what it printed, readable after the fact (post-mortems, timeouts).
export function FinishedFeed({ stepId }: { stepId: string }) {
  const [open, setOpen] = useState(false)
  const { data, err } = useGet<StepProgress>(open ? `/steps/${stepId}/live?from=0` : null)
  const lines = data ? data.lines ?? [] : null
  return (
    <details style={{ marginTop: 6 }} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary>Terminal</summary>
      {err ? <div className="error small">The activity could not be read: {err}</div>
        : lines === null ? <div className="muted small">…</div>
        : lines.length === 0 ? <div className="muted small">no terminal output recorded</div>
        : <pre className="small" style={{ maxHeight: 300, overflow: 'auto', whiteSpace: 'pre-wrap' }}>{lines.join('\n')}</pre>}
    </details>
  )
}
