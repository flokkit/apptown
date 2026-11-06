import { Disclosure } from '../Popover'
import { ListRow } from '../ListRow'
import { Section } from '../Section'
import './People.css'
import { Tabs } from '../Tabs'
import { Link, useSearchParams } from 'react-router-dom'
import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { HttpError, del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate, ReadStatus, StaleNote } from '../ReadGate'
import { QuietConfirm, QuietPrompt } from '../QuietConfirm'

import type { AuthMe, Account, AdministratorNetworksView, GrantList, GrantView, Group, GroupList, InviteLink, InviteList, RoleCatalog, RoleView, UserList } from '../wire.gen'
import { useHolds } from '../instancePages'
import { grantTitle, subjectLabel, groupLabel } from '../parts/grants'
import { fmtStamp, fmtWhen } from '../format'
import { PageHead } from '../PageHead'
import { AgentsCard, TokensCard } from './AccountAccess'
import { ExternalProviderSettings } from './ExternalSignIn'

// The server's shapes, generated from its Go types (wire.gen.ts).
export type Role = RoleView

// People: who can act here, and the groups that stand in for them in
// approval steps. An approval step naming "group:reviewers" is satisfied by
// any member — the step cites the group, the ledger records the person.
export default function People() {
  const [params] = useSearchParams()
  const tabs = [['people', 'Accounts'], ['groups', 'Groups and roles'], ['signin', 'Sign-in'], ['agents', 'Agents and tokens']] as const
  const selected = params.get('tab') || 'people'
  const tab = tabs.some(([key]) => key === selected) ? selected : 'people'
  const usersQ = useGet<UserList>('/users')
  const groupsQ = useGet<GroupList>('/groups')
  const rolesQ = useGet<RoleCatalog>('/authz/roles')
  const grantsQ = useGet<GrantList>('/authz/grants?scope=all')
  // The page is readable by everyone subject to it (clear lines of
  // decision-making); only admins get the controls.
  const meQ = useGet<AuthMe>('/auth/me')
  // Each card waits for the reads it shows: a list never read is not an
  // empty one.
  const users = usersQ.data?.users ?? [], groups = groupsQ.data?.groups ?? []
  const roles = rolesQ.data?.roles ?? [], grants = grantsQ.data?.grants ?? []
  // Each card's controls follow the capability its writes need; until the
  // AppTown has answered, nobody sees them.
  const canUsers = useHolds('identity:manage-users')
  const canGroups = useHolds('identity:manage-groups')
  const canGrants = useHolds('authorization:manage-grants')
  const canEdit = useHolds('identity:manage-auth')
  const { run, err } = useAction()

  const act = (fn: () => Promise<unknown>) => { void run(fn) }

  return (
    <div className="people-div">
      <PageHead page="people" />
      {err && <div className="card error">{err}</div>}

      {!canEdit && !canUsers && !canGroups && !canGrants && (
        <div className="muted small">
          Only an admin can change these settings.
        </div>
      )}
      <Tabs className="tabs people-tabs" aria-label="People settings">
        {tabs.map(([key, label]) => <Link key={key} to={key === 'people' ? '/people' : `/people?tab=${key}`}
          className={'tab' + (tab === key ? ' active' : '')} aria-current={tab === key ? 'page' : undefined}>{label}</Link>)}
      </Tabs>
      {tab === 'people' && <ReadGate q={usersQ} what="the accounts">
        <UsersCard users={users} act={act} canEdit={canUsers} invites={meQ.data?.mode === 'internal'} sso={!!meQ.data && meQ.data.mode !== 'internal'} me={meQ.data?.user ?? ''} />
      </ReadGate>}
      {tab === 'groups' && <>
      <ReadGate q={groupsQ} also={[usersQ]} what="the groups">
        <GroupsCard groups={groups} users={users} act={act} canEdit={canGroups} />
      </ReadGate>
      <ReadGate q={rolesQ} also={[grantsQ, usersQ, groupsQ]} what="the roles and grants">
        <RolesCard roles={roles} grants={grants} users={users} groups={groups} act={act} canEdit={canGrants} />
      </ReadGate>
      </>}
      {tab === 'signin' && <>
      {canUsers && meQ.data?.mode === 'internal' && <ExternalProviderSettings />}
      <AdminNetworksCard canEdit={canEdit} />
      </>}
      {tab === 'agents' && <>
        <Link to="/account">Your tokens and connected agents →</Link>
        {canEdit && <AgentsCard canEdit />}
        {canEdit && <TokensCard />}
      </>}
    </div>
  )
}


// Where administrators may sign in: a list of networks an administrator
// account must be connecting from to sign in, connect an agent, or use an
// administrator token. Empty means anywhere; loopback always passes. The
// server refuses a list that would lock out the person setting it.
export function AdminNetworksCard({ canEdit }: { canEdit: boolean }) {
  const q = useGet<AdministratorNetworksView>('/auth/admin-networks')
  const nets = q.data?.networks ?? [], you = q.data?.you ?? '', inside = q.data?.youInside ?? true
  const [draft, setDraft] = useState('')
  const { run, err: actErr, failure } = useAction(undefined, { affects: ['/auth/admin-networks'] })
  // Each change is saved at once, from the list as this page read it; if
  // someone saved a different list meanwhile, it is refused and read again.
  const moved = failure instanceof HttpError && failure.code === 'settings-moved'
  const reload = q.reload
  useEffect(() => { if (moved) void reload() }, [moved, reload])
  const err = moved ? 'The list changed since this page read it; here is the current list. Make your change again.' : actErr
  const editable = canEdit && q.current
  const save = async (networks: string[]) => (await run(() => put('/auth/admin-networks', { expected: nets, networks }))) !== undefined
  return (
    <Section title="Where administrators may sign in" note={`An administrator account can sign in, connect a coding agent, or use an administrator token only from these networks. Members are not limited. Empty means anywhere; the machine ${PRODUCT} runs on always passes.`}>
    <div className="people-agents-card-div">
      <div className="muted small">
        You are connecting from <code>{you || '…'}</code>{nets.length > 0 && (inside ? ', which is inside' : ', which is outside the list')}.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the administrator networks" className="small" />
      <StaleNote q={q} />
      {q.data && <div className="row small people-admin-networks-card-div">
        {nets.length === 0 && <span className="muted">Anywhere</span>}
        {nets.map((n) => (
          <span key={n} className="badge">{n}{editable && <>{' '}
            <button className="subtle" aria-label={`remove ${n}`} onClick={() => { void save(nets.filter((x) => x !== n)) }}>×</button></>}
          </span>
        ))}
        {editable && (
          <>
            <input aria-label="Network" placeholder="network, e.g. 192.168.1.0/24" value={draft} onChange={(e) => setDraft(e.target.value)} className="people-admin-networks-card-input" />
            <button disabled={!draft.trim()} onClick={async () => { if (await save([...nets, draft.trim()])) setDraft('') }}>Add</button>
          </>
        )}
      </div>}
    </div>
    </Section>
  )
}

// Who may do what on the whole AppTown: the instance roles, as grants on the
// record (design/subsystems/authorization-capabilities.md). Project roles live on each
// project's Members tab. Every grant here was made by someone, or by the
// upgrade that turned the old implicit powers into visible ones.
function RolesCard({ roles, grants, users, groups, act, canEdit }: {
  roles: Role[]; grants: GrantView[]; users: Account[]; groups: Group[]
  act: (fn: () => Promise<unknown>) => void; canEdit: boolean
}) {
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('')
  const grantees = [...users.filter(u => !u.disabled).map(u => u.username), ...groups.map(g => `group:${g.name}`)]
  const instance = grants.filter(g => g.scopeType === 'instance' || g.scopeType === 'release-target')
  const offered = roles.filter(r => r.surfaced && r.scope === 'instance')
  const byRole = new Map<string, GrantView[]>()
  for (const g of instance) {
    const k = g.role ? grantTitle(g) : 'Additional access'
    byRole.set(k, [...(byRole.get(k) || []), g])
  }
  return (
    <div className="card people-agents-card-div">
      <b>Roles in {PRODUCT}</b>
      <div className="muted small">
        Roles grant permissions. Project roles are under Settings › Members.
      </div>
      <details className="small"><summary>What each role can do</summary>
        <p className="muted small">Instance administrators act in every project and may approve their own changes. Each project can require other people to get an independent approval.</p>
        {roles.filter(r => instance.some(g => g.role === r.name)).map(r => <div key={r.name}>
          <strong>{r.title}</strong><p>{r.description}</p><ul>{r.capabilities.map(c => <li key={c}><code>{c}</code></li>)}</ul>
        </div>)}
        {instance.filter(g => !g.role && g.capability).map(g => <p key={g.id}>{subjectLabel(g)}: <code>{g.capability}</code></p>)}
      </details>
      {byRole.size === 0 && <p className="muted small">No roles assigned.</p>}
      {[...byRole.entries()].map(([title, gs]) => (
        <div key={title} className="row">
          <span className="badge" title={roles.find(r => r.title === title)?.description}>{title}</span>
          {gs.map(g => (
            <span key={g.id} className="badge" title={[g.reason, g.origin && `origin: ${g.origin}`, `granted by ${g.grantedBy}`].filter(Boolean).join(' · ')}>
              {subjectLabel(g)}
              {g.origin === 'profile:transparent' && <span className="muted"> · everyone can read</span>}
              {g.origin === 'profile:open' && <span className="muted"> · open mode</span>}
              {canEdit && g.origin !== 'profile:transparent' && <>{' '}
                <button className="subtle" aria-label={`revoke ${title} from ${subjectLabel(g)}`}
                  onClick={() => act(() => del(`/authz/grants/${g.id}`))}>×</button>
              </>}
            </span>
          ))}
        </div>
      ))}
      {canEdit && (
        <div className="people-tokens-card-div">
          <select aria-label="Subject" value={subject} onChange={e => setSubject(e.target.value)}>
            <option value="">who…</option>
            {grantees.map(g => <option key={g} value={g}>{g.startsWith('group:') ? groupLabel(g.slice(6)) : g}</option>)}
          </select>
          <select aria-label="Role to grant" value={role} onChange={e => setRole(e.target.value)}>
            <option value="">gets the role…</option>
            {offered.map(r => <option key={r.name} value={r.name} title={r.description}>{r.title}</option>)}
            <option value="release-manager">Release manager</option>
            <option value="standards-administrator">Standards administrator (guidance, packs, policies)</option>
            <option value="execution-administrator">Execution administrator (workflows, checks, scanners)</option>
          </select>
          <button disabled={!subject || !role} onClick={() => act(() => post('/authz/grants', {
            subject, role, scope: role === 'release-manager' ? 'release-target' : 'instance', scopeId: role === 'release-manager' ? '*' : '',
          }).then(() => { setSubject(''); setRole('') }))}>Grant</button>
        </div>
      )}

    </div>
  )
}

// sso says AppTown is known not to use its own sign-in, so it holds no
// passwords to reset; invites says it is known to.
export function UsersCard({ users, act, canEdit, invites, sso = false, me = '' }: { users: Account[]; act: (fn: () => Promise<unknown>) => void; canEdit: boolean; invites: boolean; sso?: boolean; me?: string }) {
  const [name, setName] = useState('')
  const [pw, setPw] = useState('')
  const [byPassword, setByPassword] = useState(false)
  // Accounts have a password, or have signed in or acted (single sign-on
  // accounts never have a password); they are disabled, never removed.
  // Everything else is a name the record has seen — open-mode actors, a
  // typo in a member list — not a person who has used AppTown.
  // Conflating the two made the table read as a breach.
  const accounts = users.filter(u => u.hasPassword || u.acted)
  const seen = users.filter(u => !u.hasPassword && !u.acted)
  // A password reset link is shown once, here; making one also lists it
  // with the invites waiting to be used.
  const [resetLink, setResetLink] = useState<InviteLink | null>(null)
  const reset = useAction(undefined, { affects: ['/invites'] })
  // One row per account: name and role, then when they were last seen and
  // the one Manage control. On a phone it is two lines.
  const accountRows = (list: Account[]) => <div className="people-accounts" role="list">
    {list.map(u => <ListRow key={u.username} listItem
      identity={<><strong>{u.username}</strong>{u.disabled && <span className="badge">Disabled</span>}<span className="people-role">{u.role === 'admin' ? 'Admin' : 'Member'}</span></>}
      state={<span className="muted small">{u.lastSeen ? `seen ${fmtStamp(u.lastSeen)}` : 'never signed in'}</span>}
      action={canEdit && <Disclosure className="account-actions" closeOnAction={false} trigger={<button className="subtle" aria-label={`Actions for ${u.username}`}>Manage</button>}>
        <div className="account-action-list">
        <label>Role                 {canEdit ? (
                  <select value={u.role}
                    onChange={e => act(() => post('/users', { username: u.username, role: e.target.value }))}>
                    <option value="member">member</option>
                    <option value="admin">admin</option>
                  </select>
                ) : <span className="badge">{u.role}</span>}
</label>
        <p className="muted small">Groups: {u.groups.join(', ') || 'None'}</p>
                {/* A reset link lets the person choose the new password
                    themselves; on use every session of theirs ends. Not for a
                    disabled account: enable it first. */}
                {canEdit && !sso && invites && !u.disabled && (
                  <QuietConfirm label="reset password" className="subtle" yes="Make link"
                    ask={`Make a password reset link for ${u.username}? You give it to them; when they use it, their old password stops working. This signs them out of browsers. Their personal access tokens and agent connections keep working · revoke those on the People page if the account may be compromised.`}
                    onYes={async () => {
                      const r = await reset.run(() => post<InviteLink>('/password-links', { username: u.username }))
                      if (r) setResetLink(r)
                    }} />
                )}
                {canEdit && !sso && (
                  <QuietPrompt label="set password" yes="Set"
                    ask={`New password for ${u.username} · their current sessions will be signed out.`}
                    fields={[{ key: 'password', type: 'password', placeholder: 'min 8 characters', required: true, minLength: 8, width: 180 }]}
                    onYes={(v) => act(() => post('/users', { username: u.username, password: v.password }))} />
                )}
                {/* Disabling keeps the account, its record and its authority;
                    it signs the person out and stops them signing in. No role
                    is sent, so authority is left as it is. Not offered on your
                    own account: AppTown would sign you out of this page. */}
                {canEdit && u.username !== me && (u.disabled ? (
                  <QuietConfirm label="enable" className="subtle" ask={`Enable ${u.username}? They can sign in again with their password.`}
                    onYes={() => act(() => post('/users', { username: u.username, disabled: false }))} />
                ) : (
                  <QuietConfirm label="disable" ask={`Disable ${u.username}? They are signed out at once and cannot sign in until enabled again. The account and its record stay.`}
                    onYes={() => act(() => post('/users', { username: u.username, disabled: true }))} />
                ))}

        </div>
      </Disclosure>} />)}
  </div>
  const active = accounts.filter(u => !u.disabled)
  const withPassword = (
    <div className="people-form">
      <input placeholder="Username" aria-label="new username" value={name} onChange={e => setName(e.target.value)} />
      <input placeholder="Password (min 8 characters)" aria-label="password" type="password" value={pw} onChange={e => setPw(e.target.value)} />
      <button className={invites ? '' : 'primary'} disabled={!name || pw.length < 8}
        onClick={() => act(() => post('/users', { username: name, password: pw }).then(() => { setName(''); setPw('') }))}>
        Add person
      </button>
    </div>
  )
  return (
    <>
    <Section title="Accounts" count={active.length} note={canEdit && sso ? 'Reset passwords through your sign-in provider.' : undefined}>
      {reset.err && <div className="error small">{reset.err}</div>}
      {resetLink && (
        <OneTimeLink made={resetLink} onDone={() => setResetLink(null)}>
          Password reset link for <b>{resetLink.invite.username}</b>. Copy it now · it is not shown again.
          Their current password and sessions keep working until they use it. Using it signs them out of browsers;
          their personal access tokens and agent connections keep working · revoke those on the People page if the account may be compromised:
        </OneTimeLink>
      )}
      {active.length === 0 && <p className="muted small">No active accounts.</p>}
      {accountRows(active)}
      {(seen.length > 0 || accounts.some(u => u.disabled)) && (
        <details>
          <summary className="muted small">
            Other accounts and names ({seen.length + accounts.filter(u => u.disabled).length})
          </summary>
          {accounts.some(u => u.disabled) && <><h3>Disabled accounts</h3>{accountRows(accounts.filter(u => u.disabled))}</>}
          {seen.length > 0 && <h3>Names without an account ({seen.length})</h3>}
          <div className="muted small people-users-card-div">
            Names that never signed in or acted. Set a password to claim one
            as a real account; remove the rest.
          </div>
          {seen.map(u => (
            <div key={u.username} className="row small people-div-3">
              <span>{u.username}</span>
              {u.role === 'admin' && <span className="badge">admin</span>}
              <span className="muted">last seen {u.lastSeen ? fmtWhen(u.lastSeen) : '—'}</span>
              <span className="grow" />
              {canEdit && <>
                <QuietPrompt label="claim" yes="Claim"
                  ask={`Set a password for ${u.username} · this makes it a sign-in account.`}
                  fields={[{ key: 'password', type: 'password', placeholder: 'min 8 characters', required: true, minLength: 8, width: 180 }]}
                  onYes={(v) => act(() => post('/users', { username: u.username, password: v.password }))} />
                <QuietConfirm label="remove" ask={`Remove the name ${u.username}? Its grants, tokens and invites are revoked; the ledger keeps its history.`}
                  onYes={() => act(() => del(`/users/${encodeURIComponent(u.username)}`))} />
              </>}
            </div>
          ))}
        </details>
      )}
    </Section>
    {/* With invites, choosing someone's password is the exception: the
        person sets their own from the link. */}
    {canEdit && invites && <InvitePerson>
      {byPassword ? <div className="people-by-password">
        <div className="muted small">You will have to pass the password on to them yourself.</div>
        {withPassword}
      </div> : <button className="link people-quiet" onClick={() => setByPassword(true)}>Set their password instead</button>}
    </InvitePerson>}
    {canEdit && !invites && <Section title="Add a person">{withPassword}</Section>}
    </>
  )
}

// Inviting a person: an administrator names the account and its role, and
// gets a link to pass on. The person opens it, sets their own password and
// is signed in. The link is shown once, here; AppTown sends no email.
export function InvitePerson({ children }: { children?: React.ReactNode }) {
  const q = useGet<InviteList>('/invites')
  const pending = q.data?.invites ?? []
  const [name, setName] = useState('')
  const [label, setLabel] = useState('')
  const [role, setRole] = useState('member')
  const [made, setMade] = useState<InviteLink | null>(null)
  const { run, busy, err } = useAction()
  const create = async () => {
    const r = await run(() => post<InviteLink>('/invites', { username: name.trim(), label: label.trim(), role }))
    if (r) { setMade(r); setName(''); setLabel(''); setRole('member') }
  }
  return (
    <Section title="Invite a person" note={`${PRODUCT} makes a link for one account that works once, for seven days. Send it yourself; they choose their own password.`}>
      <div className="people-invite">
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the invites waiting" className="small" />
      <StaleNote q={q} />
      {made && (
        <OneTimeLink made={made} onDone={() => setMade(null)}>
          Invite for <b>{made.invite.username}</b> ({made.invite.role}). Copy the link now · it is not shown again:
        </OneTimeLink>
      )}
      <div className="people-form">
        <input placeholder="Username" aria-label="username" value={name} onChange={e => setName(e.target.value)} />
        <input placeholder="Name or email" aria-label="name or email (optional)" value={label} onChange={e => setLabel(e.target.value)}
          title={`optional; a label for you; ${PRODUCT} sends no email`} />
        <select aria-label="role" value={role} onChange={e => setRole(e.target.value)}>
          <option value="member">member</option>
          <option value="admin">admin</option>
        </select>
        <button className="primary" disabled={!name.trim() || busy} onClick={() => { void create() }}>Create invite link</button>
      </div>
      {pending.length > 0 && (
        <div className="people-invite-person-div">
          <div className="muted small">Waiting to be used</div>
          {pending.map(i => (
            <div key={i.id} className="row small people-admin-networks-card-div">
              <span className="badge">{i.username}</span>
              {i.label && <span>{i.label}</span>}
              <span className="muted">
                {i.purpose === 'password' ? `password reset · made by ${i.invitedBy}` : `${i.role} · invited by ${i.invitedBy}`} · expires {fmtWhen(i.expiresAt)}
              </span>
              <span className="grow" />
              <QuietConfirm label="withdraw" ask={`Withdraw the ${i.purpose === 'password' ? 'password reset link' : 'invite'} for ${i.username}? The link stops working.`}
                onYes={() => { void run(() => del(`/invites/${i.id}`)) }} />
            </div>
          ))}
        </div>
      )}
      {children}
      </div>
    </Section>
  )
}

// OneTimeLink shows a link AppTown returns once — an invite or a password
// reset link — with a copy button. AppTown keeps only its hash.
// "Copied" belongs to the exact link copied: a newer link shown in the same
// place says Copy link until it has been copied itself.
function OneTimeLink({ made, onDone, children }: { made: InviteLink; onDone: () => void; children: React.ReactNode }) {
  const [copiedLink, setCopiedLink] = useState('')
  const copied = copiedLink === made.link
  const copy = async () => {
    try { await navigator.clipboard.writeText(made.link); setCopiedLink(made.link) } catch { setCopiedLink('') }
  }
  return (
    <div className="card tone-ok people-one-time-link-card-tone-ok">
      <div className="small">{children}</div>
      <code className="people-tokens-card-code">{made.link}</code>
      <div className="row small people-one-time-link-div">
        <button onClick={() => { void copy() }}>{copied ? 'Copied' : 'Copy link'}</button>
        <button className="subtle" onClick={onDone}>Done</button>
      </div>
    </div>
  )
}

function GroupsCard({ groups, users, act, canEdit }: {
  groups: Group[]; users: Account[]; act: (fn: () => Promise<unknown>) => void; canEdit: boolean
}) {
  const [name, setName] = useState('')
  return (
    <div className="card people-agents-card-div">
      <b>Groups</b>
      {groups.length === 0 && <div className="muted small">No groups yet.</div>}
      {groups.filter(g => g.kind === 'built-in' || g.kind === 'personal').map(g => (
        <div key={g.name} className="row small people-div-4">
          <span className="badge">{groupLabel(g.name)}</span>
          <span className="muted">
            {g.kind === 'built-in' ? 'everyone who is signed in; grant this group a role to give everyone that access'
              : 'Personal group'}
          </span>
        </div>
      ))}
      {groups.filter(g => g.kind !== 'built-in' && g.kind !== 'personal').map(g => (
        <div key={g.name} className="people-div-5">
          <span className="badge">{groupLabel(g.name)}</span>
          {canEdit ? users.map(u => {
            const inG = g.members.includes(u.username)
            return (
              <label key={u.username} className="small people-label">
                <input type="checkbox" checked={inG}
                  onChange={() => act(() => post('/groups', {
                    name: g.name, description: g.description,
                    members: inG ? g.members.filter(m => m !== u.username) : [...g.members, u.username],
                  }))} />
                {u.username}
              </label>
            )
          }) : <span className="muted small">{g.members.join(', ') || 'no members'}</span>}
          <span className="grow" />
          {canEdit && (
            <QuietConfirm label="delete" ask={`Delete group ${g.name}? Approval steps naming group:${g.name} will stop matching anyone.`}
              onYes={() => act(() => del(`/groups/${g.name}`))} />
          )}
        </div>
      ))}
      {canEdit && <div className="people-tokens-card-div">
        <input aria-label="New group name" placeholder="new group name (e.g. reviewers)" value={name} onChange={e => setName(e.target.value)} />
        <button disabled={!name}
          onClick={() => act(() => post('/groups', { name, members: [] }).then(() => setName('')))}>
          Add group
        </button>
      </div>}
    </div>
  )
}
