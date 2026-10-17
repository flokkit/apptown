import { useState } from 'react'
import { del, post, put } from '../../../api'
import { grantTitle, subjectLabel } from '../../../parts/grants'
import { firstErr, useAction, useGet } from '../../../data'
import { useHolds, useInstancePages } from '../../../instancePages'
import type { AuthzExplanation, GroupList, ProjectDetail, ProjectMemberList, RoleCatalog, UserList } from '../../../wire.gen'

// Who works here, and as what. A member holds a project role — viewer,
// contributor, maintainer, approver, owner — and the role says what they
// may do (design/subsystems/authorization-capabilities.md §5). Reading is open to
// everyone signed in; roles gate acting.
export function MembersTab({ slug }: { slug: string }) {
  // "Who can do this?" (design/subsystems/authorization-capabilities.md §13): the
  // two questions people actually ask on a project.
  const approveQ = useGet<AuthzExplanation>(`/authz/explain?capability=change:approve&project=${slug}`)
  const landQ = useGet<AuthzExplanation>(`/authz/explain?capability=change:land&project=${slug}`)
  const projectQ = useGet<ProjectDetail>(`/projects/${slug}`)
  const membersQ = useGet<ProjectMemberList>(`/projects/${slug}/members`)
  const rolesQ = useGet<RoleCatalog>('/authz/roles')
  // The forge's list of people and groups is instance information: a project
  // owner who cannot read it names the person instead of picking from it.
  const directory = useInstancePages()('people')
  const usersQ = useGet<UserList>(directory ? '/users' : null)
  const groupsQ = useGet<GroupList>(directory ? '/groups' : null)
  const grants = membersQ.data?.roles ?? []
  const open = membersQ.data?.open ?? false
  const roles = (rolesQ.data?.roles ?? []).filter(r => r.scope === 'project' && r.surfaced)
  const options = [...(usersQ.data?.users ?? []).map(x => x.username), ...(groupsQ.data?.groups ?? []).map(x => `group:${x.name}`)]
  const independent = projectQ.data ? projectQ.data.requireIndependentApproval : null
  const canEdit = useHolds('project:manage-members', slug)
  const canConfigure = useHolds('project:configure', slug)
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('project-contributor')
  const { run, err: actErr } = useAction()
  const directoryHidden = directory === false || usersQ.denied || groupsQ.denied
  const err = firstErr(actErr, projectQ.err, membersQ.err, rolesQ.err, usersQ.denied ? '' : usersQ.err, groupsQ.denied ? '' : groupsQ.err)
  const names = (q: { data: { whoCan: string[] } | null; err: string }) =>
    q.err ? 'unknown (could not be read)' : !q.data ? '…' : q.data.whoCan.length ? q.data.whoCan.join(', ') : 'nobody yet'
  const act = (fn: () => Promise<unknown>) => { void run(fn) }
  return (
    <div className="card" style={{ display: 'grid', gap: 10, marginTop: 10 }}>
      <b>Approval policy</b>
      <label className="small">
        <input type="checkbox" checked={independent ?? false} disabled={!canConfigure || independent === null}
          onChange={e => act(() => put(`/projects/${slug}/approval-policy`, { requireIndependentApproval: e.target.checked }))} />
        {' '}Require another person to approve the author's change
      </label>
      <div className="muted small">Applies to this project. Instance administrators are exempt from author and approver-list restrictions. The required number of votes still applies.</div>
      <b>Members</b>
      <div className="muted small">
        A <b>contributor</b> opens changes and
        hands them off; a <b>maintainer</b> also lands them and changes the
        project's setup; an <b>approver</b> says yes at approval steps; an{' '}
        <b>owner</b> manages the project and its members. Everyone
        signed in can read.
      </div>
      {err && <div className="small" style={{ color: 'var(--bad)' }}>{err}</div>}
      {open && (
        <div className="small">
          <span className="chip">open to everyone</span>{' '}
          <span className="muted">Everyone signed in holds the group roles listed below. Remove a group role explicitly to change that access.</span>
        </div>
      )}
      <div style={{ display: 'grid', gap: 6 }}>
        {grants.length === 0 && !open && <span className="muted small">nobody holds a role here yet</span>}
        {grants.map(g => (
          <div key={g.id} className="row small" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <span className="chip">{subjectLabel(g)}</span>
            <span title={[g.reason, g.origin && `origin: ${g.origin}`, `granted by ${g.grantedBy}`].filter(Boolean).join(' · ')}>
              {grantTitle(g)}
            </span>
            <span style={{ flex: 1 }} />
            {canEdit && (
              <button className="subtle" aria-label={`remove ${grantTitle(g)} from ${subjectLabel(g)}`}
                onClick={() => act(() => del(`/authz/grants/${g.id}`))}>×</button>
            )}
          </div>
        ))}
      </div>
      <div className="muted small">
        Who can approve here: {names(approveQ)}.
        Who can land: {names(landQ)}.
        {' '}<span title="group:all-authenticated-users means everyone signed in">(a group name means any of its members)</span>
      </div>
      {canEdit && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {directoryHidden ? (
            <input value={subject} placeholder="username, or group:name" aria-label="who to add"
              onChange={e => setSubject(e.target.value.trim())} />
          ) : (
            <select value={subject} onChange={e => setSubject(e.target.value)}>
              <option value="">add…</option>
              {options.map(o => <option key={o} value={o}>{o}</option>)}
            </select>
          )}
          <select value={role} onChange={e => setRole(e.target.value)}>
            {roles.map(r => <option key={r.name} value={r.name} title={r.description}>{r.title}</option>)}
          </select>
          <button disabled={!subject}
            onClick={() => act(() => post(`/projects/${slug}/members`, { grantee: subject, role }).then(() => setSubject('')))}>
            Add
          </button>
        </div>
      )}
    </div>
  )
}
