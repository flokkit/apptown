import { forwardRef, type ComponentPropsWithoutRef } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from './data'
import type { ChangeView } from './api'
import { PRODUCT } from './brand'
import { ReadStatus } from './ReadGate'
import { DetailsPopover } from './Popover'
import { ChangeStatus } from './Status'
import type { Approver, FailureAdvice, StepApprovers, WaitingOn } from './wire.gen'
import './styles/status-details.css'
import './Approvers.css'

// A small team lists everyone who can approve, under the role or group that
// lets them; past this many people a role or group shows its count and links
// to the group instead.
const listEveryoneBelow = 5

function people(a: Approver): string[] {
  return [...a.people, ...(a.groups ?? []).flatMap(people)]
}

const groupLink = (name: string) => `/people/groups/${encodeURIComponent(name)}`

function Entry({ a, everyone }: { a: Approver; everyone: boolean }) {
  const count = people(a).length
  const title = a.kind === 'person' ? <strong>{a.name}</strong>
    : a.kind === 'group' ? <><Link to={groupLink(a.name)}><strong>{a.label}</strong></Link> <span className="muted">group</span></>
    : <><strong>{a.label}</strong> <span className="muted">role on this project</span></>
  if (a.kind === 'person') return <li>{title}</li>
  if (a.everyone) return <li>{title}<div className="muted">Everyone with an account</div></li>
  const nobody = count === 0 && !(a.groups ?? []).some(g => g.everyone)
  return <li>
    {title}
    {nobody ? <div className="muted">{a.kind === 'role' ? 'Nobody holds this role yet' : 'No members yet'}</div>
      : everyone ? <>
        {a.people.length > 0 && <ul>{a.people.map(p => <li key={p}>{p}</li>)}</ul>}
        {(a.groups ?? []).length > 0 && <ul>{a.groups!.map(g => <Entry key={g.name} a={g} everyone={everyone} />)}</ul>}
      </>
      // Too many to list: people named on a role still are; a group is its count.
      : a.kind === 'group' ? <div className="muted">{count} {count === 1 ? 'member' : 'members'}</div>
      : <ul>
        {a.people.map(p => <li key={p}>{p}</li>)}
        {(a.groups ?? []).map(g => <li key={g.name}><Link to={groupLink(g.name)}>{g.label}</Link> <span className="muted">group · {g.everyone ? 'everyone' : `${people(g).length} ${people(g).length === 1 ? 'member' : 'members'}`}</span></li>)}
      </ul>}
  </li>
}

// ApproversCard is who can decide an approval step now, people resolved from
// its roles and groups by the server, as eligibility reads them.
export function ApproversCard({ stepId, inline = false }: { stepId: string | null; inline?: boolean }) {
  const q = useGet<StepApprovers>(stepId ? `/steps/${stepId}/approvers` : null)
  if (!stepId) return <p>Who can approve is not known.</p>
  if (!q.data) return <ReadStatus q={q} what="who can approve" />
  if (!Array.isArray(q.data.approvers)) return <p>Who can approve could not be read.</p>
  const all = new Set(q.data.approvers.flatMap(people))
  const everyone = all.size < listEveryoneBelow
  return <div className="approvers-card">
    {!inline && <h3>Who can approve</h3>}
    <ul>{q.data.approvers.map(a => <Entry key={`${a.kind}:${a.name}`} a={a} everyone={everyone} />)}</ul>
    {q.data.opener && <p className="small muted">Not {q.data.opener}, who opened it: this project needs someone else to approve.</p>}
    <p className="small muted">{PRODUCT} administrators can also approve.</p>
  </div>
}

const WaitingButton = forwardRef<HTMLButtonElement, { waiting: WaitingOn; about: string } & ComponentPropsWithoutRef<'button'>>(function WaitingButton({ waiting, about, ...button }, ref) {
  return <ChangeStatus waiting={waiting} button={{ ...button, ref, 'aria-label': `${waiting.says}: ${about}` }} />
})

// The first sentence of a recorded problem, short enough to read at a glance.
const firstSentence = (text: string) => {
  const line = text.split('\n')[0].trim()
  const sentence = line.match(/^.+?[.!?](?=\s|$)/)?.[0] ?? line
  return sentence.length > 180 ? `${sentence.slice(0, 177)}…` : sentence
}

// FailureWhy is what went wrong at a failed step, leading with the problem.
// The change page's advice says it in plain words; where there is none (no
// model, or none written yet) the step's own record does: a review's first
// problem, else the reason the step gave. A step that gave none says so and
// points at its output on the change.
function FailureWhy({ stepId, changeId }: { stepId: string; changeId?: string | null }) {
  const q = useGet<FailureAdvice>(`/steps/${stepId}/advice`)
  const change = useGet<ChangeView>(changeId && q.data && !q.data.text ? `/changes/${changeId}` : null)
  const step = change.data?.detail?.steps.find(s => s.id === stepId)
  const said = step?.evidence as { findings?: string[]; reason?: string } | undefined
  const findings = (said?.findings ?? []).filter(f => f.trim())
  const reason = said?.reason?.replace(/^reviewer rejected:\s*/i, '') ?? ''
  return <div className="status-why">
    {q.data?.text ? <p>{q.data.text}</p>
      : findings.length ? <><p className="muted small">{findings.length === 1 ? 'The reviewer found a problem:' : `The reviewer found ${findings.length} problems. The first:`}</p><p>{firstSentence(findings[0])}</p></>
      : step && reason ? <p>{firstSentence(reason)}</p>
      : step ? <p>This check failed without saying why. Its output is on the change.</p>
      : q.data && !changeId ? <p className="muted">No reason was recorded for this failure.</p>
      : <ReadStatus q={changeId && q.data ? change : q} what="what went wrong" />}
    {changeId && <Link to={`/changes/${changeId}`}>Open the change →</Link>}
  </div>
}

// hasDetails: a change status that opens something — who can approve an
// approval, or what went wrong at a failed step.
export const hasDetails = (w: WaitingOn) => !!w.step && (w.kind === 'approval' || w.tone === 'bad')

// WaitingStatus is a change's status chip that opens its details: who can
// approve, or what went wrong. Anything else is the plain chip.
export function WaitingStatus({ waiting, changeId }: { waiting: WaitingOn; changeId?: string | null }) {
  if (!hasDetails(waiting)) return <ChangeStatus waiting={waiting} />
  const approval = waiting.kind === 'approval'
  return <DetailsPopover className="approval-status" closeOnAction={false} title={<h2>{waiting.says}</h2>} trigger={
    <WaitingButton waiting={waiting} about={approval ? 'who can approve' : 'what went wrong'} />
  }>{approval ? <ApproversCard stepId={waiting.step} /> : <FailureWhy stepId={waiting.step!} changeId={changeId} />}</DetailsPopover>
}

