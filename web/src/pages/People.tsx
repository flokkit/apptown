import { ListRow } from '../ListRow'
import { FormMenu } from '../MoreMenu'
import { ListTable } from '../ListTable'
import { AccountStatus } from '../Status'
import { Modal } from '../Modal'
import { Section } from '../Section'
import './People.css'
import { Link, useLocation, useParams, useNavigate } from 'react-router-dom'
import { PRODUCT } from '../brand'
import { Fragment, useEffect, useState } from 'react'
import { HttpError, del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate, ReadStatus, StaleNote } from '../ReadGate'
import { QuietConfirm, QuietPrompt } from '../QuietConfirm'

import type { AuthMe, Account, AdministratorNetworksView, GrantList, GrantView, Group, GroupList, InviteLink, InviteList, RoleCatalog, RoleView, UserList } from '../wire.gen'
import { useHolds } from '../instancePages'
import { grantTitle, subjectLabel, groupLabel } from '../parts/grants'
import { fmtStamp, fmtWhen, relTime } from '../format'
import { PageHead } from '../PageHead'
import { AgentsCard, TokensCard } from './AccountAccess'
import { ExternalProviderSettings } from './ExternalSignIn'

// The server's shapes, generated from its Go types (wire.gen.ts).
export type Role = RoleView

// People: who can act here, and the groups that stand in for them in
// approval steps. An approval step naming "group:reviewers" is satisfied by
// any member — the step cites the group, the ledger records the person.
export const PEOPLE_PAGES = [['accounts', 'Accounts'], ['groups', 'Groups'], ['roles', 'Roles'], ['sign-in', 'Sign-in'], ['agents-and-tokens', 'Agents and tokens']] as const

export default function People({ view = 'accounts' }: { view?: typeof PEOPLE_PAGES[number][0] }) {
  const { name } = useParams()
  const location = useLocation()
  const tab = view
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
  const { run, err } = useAction(undefined, { affects: ['/users', '/groups', '/authz', '/auth/me'] })

  const act = (fn: () => Promise<unknown>) => { void run(fn) }

  const pageReads = view === 'accounts' ? [usersQ] : view === 'groups' ? [groupsQ, usersQ, grantsQ] : view === 'roles' ? [rolesQ, grantsQ, usersQ, groupsQ] : []
  const pageReady = pageReads.every(q => q.data != null)

  if (new URLSearchParams(location.search).has('tab')) return <h1>Page not found</h1>
  return (
    <div className="people-div">
      {(!pageReady || view === 'sign-in' || view === 'agents-and-tokens') && <PageHead page="people" sub={PEOPLE_PAGES.find(([key]) => key === view)?.[1]} title={name ?? PEOPLE_PAGES.find(([key]) => key === view)?.[1]} />}
      {err && <div className="card error">{err}</div>}

      {!canEdit && !canUsers && !canGroups && !canGrants && (
        <div className="muted small">
          Only an admin can change these settings.
        </div>
      )}
      {tab === 'accounts' && <ReadGate q={usersQ} what="the accounts">
        <UsersCard users={name ? users.filter(u => u.username === name) : users} act={act} canEdit={canUsers} invites={meQ.data?.mode === 'internal'} sso={!!meQ.data && meQ.data.mode !== 'internal'} me={meQ.data?.user ?? ''} detail={!!name} actionError={err} />
      </ReadGate>}
      {tab === 'groups' && <ReadGate q={groupsQ} also={[usersQ, grantsQ]} what="the groups">
        <GroupsCard groups={groups} users={users} grants={grants} act={act} canEdit={canGroups} name={name} actionError={err} />
      </ReadGate>}
      {tab === 'roles' && <ReadGate q={rolesQ} also={[grantsQ, usersQ, groupsQ]} what="the roles and grants">
        <RolesCard roles={roles} grants={grants} users={users} groups={groups} act={act} canEdit={canGrants} actionError={err} />
      </ReadGate>}
      {tab === 'sign-in' && <>
      {canUsers && meQ.data?.mode === 'internal' && <ExternalProviderSettings />}
      <AdminNetworksCard canEdit={canEdit} />
      </>}
      {tab === 'agents-and-tokens' && <>
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
function RolesCard({ roles, grants, users, groups, act, canEdit, actionError }: {
  roles: Role[]; grants: GrantView[]; users: Account[]; groups: Group[]
  act: (fn: () => Promise<unknown>) => void; canEdit: boolean; actionError?: string
}) {
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('')
  const grantees = [...users.filter(u => !u.disabled).map(u => u.username), ...groups.map(g => `group:${g.name}`)]
  const instance = grants.filter(g => g.scopeType === 'instance' || g.scopeType === 'release-target')
  const offered = roles.filter(r => r.surfaced && r.scope === 'instance')
  const [granting, setGranting] = useState(false)
  const [selectedRole, setSelectedRole] = useState<Role | null>(null)
  return (
    <>
    <PageHead page="people" sub="Roles" title="Roles" actions={canEdit && <button className="primary" onClick={() => setGranting(true)}>Grant role</button>} />
    <Section title="All roles" count={instance.length} label="Roles">
      {!instance.length && <p>No roles assigned.</p>}
      <ListTable label="Roles granted" rows={instance} rowKey={g => String(g.id)} columns={[
        { key: 'role', label: 'Role', phone: 'name', render: g => {
          const role = roles.find(r => r.name === g.role)
          return role ? <button className="record-name" title={grantTitle(g)} onClick={() => setSelectedRole(role)}>{grantTitle(g)}</button> : grantTitle(g) || 'Additional access'
        } },
        { key: 'subject', label: 'Granted to', phone: 'state', render: g => <span title={subjectLabel(g)}>{subjectLabel(g)}</span> },
        { key: 'scope', label: 'Scope', render: g => <span title={`${g.scopeType} ${g.scopeId}`}>{g.scopeId || g.scopeType}</span> },
        { key: 'by', label: 'Granted by', render: g => <span title={[g.reason, g.origin && `origin: ${g.origin}`, `granted by ${g.grantedBy}`].filter(Boolean).join(' · ')}>{g.grantedBy}{g.origin === 'profile:transparent' ? ' · everyone can read' : g.origin === 'profile:open' ? ' · open mode' : ''}</span> },
        { key: 'action', label: '', phone: 'action', render: g => canEdit && g.origin !== 'profile:transparent' && <button className="subtle" aria-label={`revoke ${grantTitle(g)} from ${subjectLabel(g)}`} onClick={() => act(() => del(`/authz/grants/${g.id}`))}>Revoke</button> },
      ]} />
      {selectedRole && <Modal title={selectedRole.title} onClose={() => setSelectedRole(null)}><p>{selectedRole.description}</p><ul>{selectedRole.capabilities.map(c => <li key={c}><code>{c}</code></li>)}</ul></Modal>}
      {canEdit && granting && (
        <Modal title="Grant role" onClose={() => setGranting(false)}>{actionError && <p className="error">{actionError}</p>}<div className="people-tokens-card-div">
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
          }).then(() => { setSubject(''); setRole(''); setGranting(false) }))}>Grant</button>
        </div></Modal>
      )}

    </Section>
    </>
  )
}

// sso says AppTown is known not to use its own sign-in, so it holds no
// passwords to reset; invites says it is known to.
export function UsersCard({ users, act, canEdit, invites, sso = false, me = '', detail = false, actionError }: { users: Account[]; act: (fn: () => Promise<unknown>) => void; canEdit: boolean; invites: boolean; sso?: boolean; me?: string; detail?: boolean; actionError?: string }) {
  const [inviting, setInviting] = useState(false)
  const [name, setName] = useState('')
  const [pw, setPw] = useState('')
  const [byPassword, setByPassword] = useState(false)
  // Accounts have a password, or have signed in or acted (single sign-on
  // accounts never have a password); they are disabled, never removed.
  // Everything else is a name the record has seen — open-mode actors, a
  // typo in a member list — not a person who has used AppTown.
  // Conflating the two made the table read as a breach.
  const rank = (u: Account) => u.disabled ? 1 : u.acted ? 0 : 2
  const accounts = [...users].sort((a, b) => rank(a) - rank(b) || a.username.localeCompare(b.username))
  // A password reset link is shown once, here; making one also lists it
  // with the invites waiting to be used.
  const [resetLink, setResetLink] = useState<InviteLink | null>(null)
  const reset = useAction(undefined, { affects: ['/invites'] })
  // One row per account: name and role, then when they were last seen and
  // the one ⋯ control, as on every row. On a phone it is two lines.
  const manage = (u: Account) => canEdit && <FormMenu className="account-actions" closeOnAction={false} trigger={<button className="subtle more-menu-button" aria-label={`Actions for ${u.username}`}>⋯</button>}>

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
                {canEdit && (u.hasPassword || u.acted) && !sso && invites && !u.disabled && (
                  <QuietConfirm label="reset password" className="subtle" yes="Make link"
                    ask={`Make a password reset link for ${u.username}? You give it to them; when they use it, their old password stops working. This signs them out of browsers. Their personal access tokens and agent connections keep working · revoke those on the People page if the account may be compromised.`}
                    onYes={async () => {
                      const r = await reset.run(() => post<InviteLink>('/password-links', { username: u.username }))
                      if (r) setResetLink(r)
                    }} />
                )}
                {canEdit && (u.hasPassword || u.acted) && !sso && (
                  <QuietPrompt label="set password" yes="Set"
                    ask={`New password for ${u.username} · their current sessions will be signed out.`}
                    fields={[{ key: 'password', type: 'password', placeholder: 'min 8 characters', required: true, minLength: 8, width: 180 }]}
                    onYes={(v) => act(() => post('/users', { username: u.username, password: v.password }))} />
                )}
                {/* Disabling keeps the account, its record and its authority;
                    it signs the person out and stops them signing in. No role
                    is sent, so authority is left as it is. Not offered on your
                    own account: AppTown would sign you out of this page. */}
                {canEdit && (u.hasPassword || u.acted) && u.username !== me && (u.disabled ? (
                  <QuietConfirm label="enable" className="subtle" ask={`Enable ${u.username}? They can sign in again with their password.`}
                    onYes={() => act(() => post('/users', { username: u.username, disabled: false }))} />
                ) : (
                  <QuietConfirm label="disable" ask={`Disable ${u.username}? They are signed out at once and cannot sign in until enabled again. The account and its record stay.`}
                    onYes={() => act(() => post('/users', { username: u.username, disabled: true }))} />
                ))}

        </div>
        {!u.hasPassword && !u.acted && <>
          <QuietPrompt label="claim" yes="Claim" ask={`Set a password for ${u.username} · this makes it a sign-in account.`}
            fields={[{ key: 'password', type: 'password', placeholder: 'min 8 characters', required: true, minLength: 8, width: 180 }]}
            onYes={v => act(() => post('/users', { username: u.username, password: v.password }))} />
          <QuietConfirm label="remove" ask={`Remove the name ${u.username}? Its grants, tokens and invites are revoked; the record keeps its history.`}
            onYes={() => act(() => del(`/users/${encodeURIComponent(u.username)}`))} />
        </>}
      </FormMenu>
  const accountRows = <ListTable label="Accounts" className="people-accounts" rows={accounts} rowKey={u => u.username} rowTo={detail ? undefined : u => `/people/accounts/${encodeURIComponent(u.username)}`} columns={[
    { key: 'name', label: 'Name', phone: 'name', render: u => <Link to={`/people/accounts/${encodeURIComponent(u.username)}`}>{u.username}</Link> },
    { key: 'role', label: 'Role', render: u => u.role === 'admin' ? 'Admin' : 'Member' },
    // On an account's own page its groups are links; in the list the row opens the account.
    { key: 'groups', label: 'Groups', render: u => detail
      ? <>{u.groups.map((g, i) => <Fragment key={g}>{i > 0 && ', '}<Link to={`/people/groups/${encodeURIComponent(g)}`}>{groupLabel(g)}</Link></Fragment>)}{!u.groups.length && 'None'}</>
      : <span title={u.groups.join(', ')}>{u.groups.join(', ') || 'None'}</span> },
    { key: 'status', label: 'Status', phone: 'state', render: u => <AccountStatus {...u} /> },
    { key: 'seen', label: 'Last seen', kind: 'time', render: u => <span title={fmtStamp(u.lastSeen ?? '')}>{relTime(u.lastSeen ?? '') || '—'}</span> },
    { key: 'manage', label: '', phone: 'action', render: manage },
  ]} />
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
    <PageHead page="people" sub={detail ? 'Account' : 'Accounts'} title={detail ? users[0]?.username ?? 'Account' : 'Accounts'} actions={!detail && canEdit && <button className="primary" onClick={() => setInviting(true)}>{invites ? 'Invite' : 'Add person'}</button>} />
    <Section title={detail ? '' : 'All accounts'} count={detail ? undefined : accounts.length} label="Accounts" note={canEdit && sso ? 'Reset passwords through your sign-in provider.' : undefined}>
      {reset.err && <div className="error small">{reset.err}</div>}
      {resetLink && (
        <OneTimeLink made={resetLink} onDone={() => setResetLink(null)}>
          Password reset link for <b>{resetLink.invite.username}</b>. Copy it now · it is not shown again.
          Their current password and sessions keep working until they use it. Using it signs them out of browsers;
          their personal access tokens and agent connections keep working · revoke those on the People page if the account may be compromised:
        </OneTimeLink>
      )}
      {accounts.length === 0 && <p className="muted small">No accounts.</p>}
      {accountRows}
    </Section>
    {/* With invites, choosing someone's password is the exception: the
        person sets their own from the link. */}
    {inviting && canEdit && invites && <Modal title="Invite a person" onClose={() => setInviting(false)}>{actionError && <p className="error">{actionError}</p>}<InvitePerson>
      {byPassword ? <div className="people-by-password">
        <div className="muted small">You will have to pass the password on to them yourself.</div>
        {withPassword}
      </div> : <button className="link people-quiet" onClick={() => setByPassword(true)}>Set their password instead</button>}
    </InvitePerson></Modal>}
    {inviting && canEdit && !invites && <Modal title="Add a person" onClose={() => setInviting(false)}>{actionError && <p className="error">{actionError}</p>}{withPassword}</Modal>}
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
            <ListRow key={i.id} className="row small people-admin-networks-card-div pending-invite-row" identity={<>
              <span className="badge">{i.username}</span>
              {i.label && <span>{i.label}</span>}
              <span className="muted">
                {i.purpose === 'password' ? `password reset · made by ${i.invitedBy}` : `${i.role} · invited by ${i.invitedBy}`} · expires {fmtWhen(i.expiresAt)}
              </span>
              </>} action={<QuietConfirm label="withdraw" ask={`Withdraw the ${i.purpose === 'password' ? 'password reset link' : 'invite'} for ${i.username}? The link stops working.`}
                onYes={() => { void run(() => del(`/invites/${i.id}`)) }} />} />
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

function GroupsCard({ groups, users, grants, act, canEdit, name, actionError }: {
  groups: Group[]; users: Account[]; grants: GrantView[]; act: (fn: () => Promise<unknown>) => void; canEdit: boolean; name?: string; actionError?: string
}) {
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState('')
  const [adding, setAdding] = useState(false)
  const [member, setMember] = useState('')
  const navigate = useNavigate()
  const rolesFor = (g: Group) => grants.filter(r => r.subjectType === 'group' && r.subject === g.name)
  const roleText = (g: Group) => rolesFor(g).map(r => `${grantTitle(r)}${r.scopeId ? ` (${r.scopeId})` : ''}`).join(', ') || 'None'
  const kind = (g: Group) => g.kind === 'built-in' ? 'everyone' : g.kind === 'personal' ? 'personal' : 'team'
  if (name) {
    const g = groups.find(g => g.name === name)
    if (!g) return <p>Group not found.</p>
    const editable = canEdit && kind(g) === 'team'
    const members = g.kind === 'built-in' ? users.filter(u => !u.disabled && (u.hasPassword || u.acted)).map(u => u.username) : g.members
    return <>
      <PageHead page="people" sub="Groups" title={g.name} line={<>{kind(g)} group. {g.description}{g.kind === 'built-in' && ' Everyone who is signed in; grant this group a role to give everyone that access.'}</>} actions={editable && <FormMenu className="more-menu" closeOnAction={false} trigger={<button aria-label={`More actions for ${g.name}`}>⋯</button>}><QuietConfirm label="Delete group" ask={`Delete group ${g.name}? Approval steps naming group:${g.name} will stop matching anyone.`}
          onYes={() => act(() => del(`/groups/${encodeURIComponent(g.name)}`).then(() => navigate('/people/groups')))} /></FormMenu>} />
      <Section title="Members" count={members.length} action={<div className="row">
        {editable && <button className="primary" onClick={() => setAdding(true)}>Add member</button>}

      </div>}>
        <ListTable label="Group members" rows={members} rowKey={m => m} columns={[
          { key: 'name', label: 'Name', phone: 'name', render: m => <Link to={`/people/accounts/${encodeURIComponent(m)}`}>{m}</Link> },
          { key: 'status', label: 'Status', phone: 'state', render: m => <AccountStatus {...users.find(u => u.username === m)} /> },
          { key: 'seen', label: 'Last seen', kind: 'time', render: m => relTime(users.find(u => u.username === m)?.lastSeen ?? '') || '—' },
          { key: 'action', label: '', phone: 'action', render: m => editable && <FormMenu className="more-menu" trigger={<button aria-label={`More actions for ${m}`}>⋯</button>}><button onClick={() => act(() => del(`/groups/${encodeURIComponent(g.name)}/members/${encodeURIComponent(m)}`))}>Remove member</button></FormMenu> },
        ]} />
      </Section>
      <Section title="Roles granted" action={<Link to="/people/roles">Manage roles</Link>}>{rolesFor(g).length ? <ListTable label="Roles granted" rows={rolesFor(g)} rowKey={r => String(r.id)} columns={[
        { key: 'role', label: 'Role', phone: 'name', render: grantTitle },
        { key: 'scope', label: 'Scope', phone: 'state', render: r => <>{r.scopeType}{r.scopeId && `: ${r.scopeId}`}</> },
        { key: 'grantor', label: 'Granted by', render: r => r.grantedBy },
        { key: 'reason', label: 'Reason', render: r => r.reason },
      ]} /> : <p>No roles granted.</p>}</Section>
      {adding && <Modal title="Add member" onClose={() => setAdding(false)}>{actionError && <p className="error">{actionError}</p>}<div className="people-form">
        <select aria-label="Account to add" value={member} onChange={e => setMember(e.target.value)}><option value="">Choose account…</option>{users.filter(u => !members.includes(u.username)).map(u => <option key={u.username}>{u.username}</option>)}</select>
        <button disabled={!member} onClick={() => act(() => post(`/groups/${encodeURIComponent(g.name)}/members/${encodeURIComponent(member)}`, {}).then(() => { setMember(''); setAdding(false) }))}>Add member</button>
      </div></Modal>}
    </>
  }
  return <>
    <PageHead page="people" sub="Groups" title="Groups" actions={canEdit && <button className="primary" onClick={() => setCreating(true)}>New group</button>} />
    <Section title="All groups" count={groups.length} label="Groups">
      {!groups.length && <p>No groups yet.</p>}
      <ListTable label="Groups" rows={groups} rowKey={g => g.name} rowTo={g => `/people/groups/${encodeURIComponent(g.name)}`} columns={[
        { key: 'name', label: 'Name', phone: 'name', render: g => <Link to={`/people/groups/${encodeURIComponent(g.name)}`}>{groupLabel(g.name)}</Link> },
        { key: 'kind', label: 'Kind', phone: 'state', render: kind },
        { key: 'members', label: 'Members', kind: 'number', render: g => g.kind === 'built-in' ? users.filter(u => !u.disabled && (u.hasPassword || u.acted)).length : g.members.length },
        { key: 'roles', label: 'Roles granted', render: g => <span title={roleText(g)}>{roleText(g)}</span> },
      ]} />
    </Section>
    {creating && <Modal title="New group" onClose={() => setCreating(false)}>{actionError && <p className="error">{actionError}</p>}<div className="people-form">
      <input aria-label="New group name" placeholder="Group name" value={draft} onChange={e => setDraft(e.target.value)} />
      <button disabled={!draft.trim()} onClick={() => act(() => post('/groups', { name: draft.trim(), members: [] }).then(() => { setDraft(''); setCreating(false) }))}>Create group</button>
    </div></Modal>}
  </>
}
