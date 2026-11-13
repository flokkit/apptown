import type { ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import type { Artifact, Step } from '../../api'
import { Elapsed } from '../../Elapsed'
import { TechnicalWords } from './TechnicalDetail'
import { stepTitle } from './Evidence'
import { StepActions } from './Steps'
import { checkResult, recordLabel, type StoryEntry } from './story'

// Evidence has its own page so opening a record never expands the list.
export function EvidenceLink({ id, runId, children }: { id: string; runId?: string; children: ReactNode }) {
  const { pathname } = useLocation()
  const base = pathname.match(/^\/changes\/[^/]+/)?.[0] ?? pathname
  return <Link to={`${base}/records/${encodeURIComponent(id)}${runId ? `?run=${encodeURIComponent(runId)}` : ''}`}>{children}</Link>
}

function RecordLink({ artifact, runId }: { artifact: Artifact; runId: string }) {
  const label = recordLabel(artifact)
  return <div className="ch-story-record" id={`record-${artifact.id}`}>
    <EvidenceLink id={`record-${artifact.id}`} runId={runId}>{label}</EvidenceLink>
  </div>
}

export function Records({ records, runId }: { records: Artifact[]; runId: string }) {
  // A record written again (an approval brief asked for twice) shows once: its latest copy.
  const latest = [...new Map([...records].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? '')).map(a => [`${a.kind}/${a.name}`, a])).values()]
  return <>{latest.map(a => <RecordLink key={a.id} artifact={a} runId={runId} />)}</>
}

// The pass/fail mark carries the result colour; the words after it stay plain.
export function ResultMark({ text }: { text: string }) {
  const tone = text.startsWith('✓') ? 'ok' : text.startsWith('✗') ? 'bad' : text.startsWith('unknown') ? 'warn' : undefined
  if (!tone) return <>{text}</>
  return <><span className={`ch-mark ${tone}`}>{tone === 'warn' ? 'unknown' : text[0]}</span>{text.slice(tone === 'warn' ? 7 : 1)}</>
}

// Every result carries a word as well as its mark.
const resultWords = (result: string) => result.replace(/^✓/, '✓ passed').replace(/^✗/, '✗ failed').replace(/^overridden by /, 'excepted by ')

export function CheckRow({ entry, technical, reload, preview, detailId, runNumber }: { entry: Pick<StoryEntry, 'step' | 'key' | 'records' | 'actionStep'>; technical: boolean; reload: () => void; preview: boolean; detailId?: string; runNumber?: number }) {
  const s = entry.step!
  const na = s.status === 'not_applicable' && !s.overrideBy
  const duration = s.startedAt && s.finishedAt ? Math.max(0, Date.parse(s.finishedAt) - Date.parse(s.startedAt)) : undefined
  return <><tr id={`entry-${entry.key}`} className={na ? 'muted' : undefined}>
    <th scope="row">{preview && <span id="preview" />}{stepTitle(s)}<TechnicalWords shown={technical}>{s.name} · {s.type}{runNumber != null && ` · run ${runNumber}`}</TechnicalWords></th>
    <td><div className="ch-check-result">{na ? checkResult(s) : <ResultMark text={resultWords(checkResult(s))} />}<Records records={entry.records} runId={s.runId!} /></div>
      {!na && !s.overrideBy && ['fail', 'error', 'needs_human'].includes(s.status) && !!s.evidence.actions?.length && <p className="ch-story-reason">To satisfy this check: {s.evidence.actions?.join(' ')}</p>}
    </td>
    <td className="ch-check-duration">{s.status === 'running' && s.startedAt ? <Elapsed startedAt={s.startedAt} /> : duration != null && `${Math.round(duration / 1000)}s`}</td>
    <td className="ch-check-actions"><div className="ch-check-links"><EvidenceLink id={detailId ?? `entry-${entry.key}`} runId={s.runId!}>details</EvidenceLink>{entry.actionStep && <StepActions step={entry.actionStep} onChange={reload} />}</div></td>
  </tr>
  </>
}

// Overview shows every current check, including skipped and non-applicable ones.
export function CheckTable({ steps, technical }: { steps: Step[]; technical: boolean }) {
 return <table className="ch-items-table" aria-label="Checks"><tbody>{steps.map(s => <CheckRow key={s.id} detailId="checks" technical={technical} reload={() => {}} preview={false} entry={{key: `${s.id}-0`, step: s, records: []}} />)}</tbody></table>
}
