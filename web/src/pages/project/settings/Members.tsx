import { useState } from 'react'
import { del, post, put } from '../../../api'
import { grantTitle, subjectLabel, groupLabel } from '../../../parts/grants'
import { firstErr, useAction, useGet } from '../../../data'
import { useHolds, useInstancePages } from '../../../instancePages'
import { Section } from '../../../Section'
import './people.css'
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
  // AppTown's list of people and groups is instance information: a project
  // owner who cannot read it names the person instead of picking from it.
  const directory = useInstancePages()('people')
  const usersQ = useGet<UserList>(directory ? '/users' : null)
  const groupsQ = useGet<GroupList>(directory ? '/groups' : null)
  const grants = membersQ.data?.roles ?? []
  const open = membersQ.data?.open ?? false
  const roles = (rolesQ.data?.roles ?? []).filter(r => r.scope === 'project' && r.surfaced)
  const options = [...(usersQ.data?.users ?? []).filter(u => !u.disabled).map(x => x.username), ...(groupsQ.data?.groups ?? []).map(x => `group:${x.name}`)]
  const independent = projectQ.data ? projectQ.data.requireIndependentApproval : null
  const canEdit = useHolds('project:manage-members', slug)
  const canConfigure = useHolds('project:configure', slug)
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('project-contributor')
  const { run, err: actErr } = useAction()
  const directoryHidden = directory === false || usersQ.denied || groupsQ.denied
  const err = firstErr(actErr, projectQ.err, membersQ.err, rolesQ.err, usersQ.denied ? '' : usersQ.err, groupsQ.denied ? '' : groupsQ.err)
  const names = (q: { data: { whoCan: string[] } | null; err: string }) =>
    q.err ? 'unknown (could not be read)' : !q.data ? '…' : q.data.whoCan.length ? q.data.whoCan.map(n => n.startsWith('group:') ? groupLabel(n.slice(6)) : n).join(', ') : 'nobody yet'
  const act = (fn: () => Promise<unknown>) => { void run(fn) }
  const subjects = [...new Set(grants.map(g => `${g.subjectType}:${g.subject}`))]
  return (
    <>
      {err && <div className="error small">{err}</div>}
      <Section title="People" count={subjects.length || undefined}
        note={<>A <b>contributor</b> opens changes and hands them off; a <b>maintainer</b> also lands
          them and changes the project's setup; an <b>approver</b> says yes at approval steps; an{' '}
          <b>owner</b> manages the project and its members. Everyone signed in can read.</>}>
        {open && (
          <div className="card row small" style={{ display: 'flex' }}>
            <span className="badge">open to everyone</span>
            <span className="muted">Group roles apply to everyone signed in. Remove a role to withdraw that access.</span>
          </div>
        )}
        {membersQ.data && subjects.length === 0 && !open && <p className="muted">Nobody holds a role here yet.</p>}
        {subjects.map(key => <MemberRow key={key} held={grants.filter(g => `${g.subjectType}:${g.subject}` === key)} canEdit={canEdit}
          onRemove={id => act(() => del(`/authz/grants/${id}`))} />)}
        {canEdit && (
          <div className="member-add">
            <label className="small">Person or group
            {directoryHidden ? (
              <input value={subject} placeholder="username, or group:name" aria-label="who to add"
                onChange={e => setSubject(e.target.value.trim())} />
            ) : (
              <select aria-label="Person or group to add" value={subject} onChange={e => setSubject(e.target.value)}>
                <option value="">add…</option>
                {options.map(o => <option key={o} value={o}>{o.startsWith('group:') ? groupLabel(o.slice(6)) : o}</option>)}
              </select>
            )}
            </label>
            <label className="small">Role
            <select aria-label="Project role" value={role} onChange={e => setRole(e.target.value)}>
              {roles.map(r => <option key={r.name} value={r.name} title={r.description}>{r.title.replace(/^Project /, '')}</option>)}
            </select>
            </label>
            <button disabled={!subject}
              onClick={() => act(() => post(`/projects/${slug}/members`, { grantee: subject, role }).then(() => setSubject('')))}>
              Add
            </button>
            {roles.find(r => r.name === role)?.description && <p className="muted small" style={{ flexBasis: '100%', margin: 0 }}>{roles.find(r => r.name === role)?.description}</p>}
          </div>
        )}
      </Section>
      <Section title="Approval">
        <div className="members-policy">
          <label className="small">
            <input type="checkbox" checked={independent ?? false} disabled={!canConfigure || independent === null}
              onChange={e => act(() => put(`/projects/${slug}/approval-policy`, { requireIndependentApproval: e.target.checked }))} />
            {' '}Require another person to approve the author's change
          </label>
          <div className="muted small">Admins may approve their own work, with one vote each.</div>
        </div>
        <div className="card row small" style={{ display: 'flex' }}><span className="member-who muted">Can approve</span><span>{names(approveQ)}</span></div>
        <div className="card row small" style={{ display: 'flex' }}><span className="member-who muted">Can land changes</span><span>{names(landQ)}</span></div>
      </Section>
    </>
  )
}

// One member: who, the roles they hold, and (for someone who may manage
// members) a Manage button that opens the roles with their Remove buttons.
function MemberRow({ held, canEdit, onRemove }: { held: ProjectMemberList['roles']; canEdit: boolean; onRemove: (id: string) => void }) {
  const [manage, setManage] = useState(false)
  return (
    <div className="card row member-row" style={{ display: 'flex' }}>
      <strong className="member-who">{subjectLabel(held[0])}</strong>
      <span className="member-roles muted small">{held.map(g => grantTitle(g).replace(/^Project /, '')).join(', ')}</span>
      {canEdit && <span className="row-action"><button aria-expanded={manage} onClick={() => setManage(m => !m)}>{manage ? 'Done' : 'Manage'}</button></span>}
      {manage && <div className="member-manage small">{held.map(g => <span key={g.id}>{grantTitle(g)} <button className="link danger" aria-label={`remove ${grantTitle(g)} from ${subjectLabel(g)}`} onClick={() => g.id && onRemove(g.id)}>Remove</button></span>)}</div>}
    </div>
  )
}
