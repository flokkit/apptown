import { Tabs } from '../Tabs'
import { Link, useSearchParams } from 'react-router-dom'
import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { HttpError, del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate, ReadStatus, StaleNote } from '../ReadGate'
import { QuietConfirm, QuietPrompt } from '../QuietConfirm'

import type { AuthMe, Account, AdministratorNetworksView, DelegationList, GrantList, GrantView, Group, GroupList, InviteLink, InviteList, IssuedToken, RoleCatalog, RoleView, TokenList, UserList } from '../wire.gen'
import { useHolds } from '../instancePages'
import { grantTitle, subjectLabel, groupLabel } from '../parts/grants'
import { fmtStamp, fmtWhen } from '../format'
import { PageHead } from '../PageHead'
import { ExternalProviderSettings, MyExternalSignIn } from './ExternalSignIn'

// The server's shapes, generated from its Go types (wire.gen.ts).
export type Role = RoleView

// People: who can act here, and the groups that stand in for them in
// approval steps. An approval step naming "group:reviewers" is satisfied by
// any member — the step cites the group, the ledger records the person.
export default function People() {
  const [params] = useSearchParams()
  const tabs = [['people', 'People'], ['groups', 'Groups and roles'], ['signin', 'Sign-in'], ['agents', 'Coding agents and tokens']] as const
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
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 20, maxWidth: 860 }}>
      <PageHead page="people" />
      {err && <div className="card error">{err}</div>}

      {!canEdit && !canUsers && !canGroups && !canGrants && (
        <div className="muted small">
          Only an admin can change these settings.
        </div>
      )}
      <Tabs className="tabs" aria-label="People settings">
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
      {meQ.data?.mode === 'internal' && <MyExternalSignIn embedded />}
      {canUsers && meQ.data?.mode === 'internal' && <ExternalProviderSettings />}
      <AdminNetworksCard canEdit={canEdit} />
      </>}
      {tab === 'agents' && <>
        <AgentsCard canEdit={canEdit} />
        {canEdit && <TokensCard />}
      </>}
    </div>
  )
}


// Coding agents people have connected through the browser sign-in
// (Docs → Connect a coding agent). Yours, or everyone's for an admin.
// Disconnecting ends the agent's credential at once.
export function AgentsCard({ canEdit }: { canEdit: boolean }) {
  const q = useGet<DelegationList>(canEdit ? '/auth/delegations?all=1' : '/auth/delegations')
  const list = q.data?.delegations ?? []
  const { run, err } = useAction()
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Connected coding agents</b>
      <div className="muted small">
        <details><summary>About connected agents</summary>
        Coding agents that signed in through the browser. Each acts as the
        person who connected it, within what they allowed.
        </details>
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the connected agents" className="small" />
      <StaleNote q={q} />
      {q.data && list.length === 0 && <span className="muted small">None yet.</span>}
      {[...new Set(list.map(d => `${d.clientName}\0${d.user}`))].map(key => {
        const connections = list.filter(d => `${d.clientName}\0${d.user}` === key).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
        const newest = connections[0]
        return <div key={key} className="agent-group">
          <div className="host-row"><strong>{newest.clientName}{connections.length > 1 ? ` ×${connections.length}` : ''} as {newest.user}</strong><span>Connected</span><span className="muted small">Latest connection {fmtStamp(newest.createdAt)}</span></div>
          <details><summary>Connections and permissions</summary>
      {connections.map(d => (
        <div key={d.id} className="row small" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="badge">{d.clientName}</span>
          <span>as <b>{d.user}</b></span>
          <span className="muted">· {d.scopes.includes('apptown:user:read') && d.scopes.length === 1 ? 'read only' : d.scopes.includes('apptown:change:create') && d.scopes.includes('apptown:change:write') ? 'Can create and edit changes' : 'Connected access'}</span>
          <span className="muted">Connected {fmtStamp(d.createdAt)}{d.lastUsed ? `, last used ${fmtStamp(d.lastUsed)}` : ''}</span><details><summary>Permissions</summary><p>{d.scopes.join(', ')}</p></details>
          <span style={{ flex: 1 }} />
          <QuietConfirm label="disconnect" ask={`Disconnect ${d.clientName} (as ${d.user})? It stops working at once; they can sign in again.`}
            onYes={() => { void run(() => del(`/auth/delegations/${d.id}`)) }} />
        </div>
      ))}</details></div>
      })}
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
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Where administrators may sign in</b>
      <div className="muted small">
        <details><summary>About administrator networks</summary>
        An administrator account can sign in, connect a coding agent, or use an
        administrator token only from these networks. Members are not limited.
        Empty means anywhere; the machine {PRODUCT} runs on always passes.
        </details>
        You are connecting from <code>{you || '…'}</code>{nets.length > 0 && (inside ? ', which is inside' : ', which is outside the list')}.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the administrator networks" className="small" />
      <StaleNote q={q} />
      {q.data && <div className="row small" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        {nets.length === 0 && <span className="muted">Anywhere</span>}
        {nets.map((n) => (
          <span key={n} className="badge">{n}{editable && <>{' '}
            <button className="subtle" aria-label={`remove ${n}`} onClick={() => { void save(nets.filter((x) => x !== n)) }}>×</button></>}
          </span>
        ))}
        {editable && (
          <>
            <input placeholder="network, e.g. 192.168.1.0/24" value={draft} onChange={(e) => setDraft(e.target.value)} style={{ width: 220 }} />
            <button disabled={!draft.trim()} onClick={async () => { if (await save([...nets, draft.trim()])) setDraft('') }}>Add</button>
          </>
        )}
      </div>}
    </div>
  )
}

// API tokens for the CLI and coding agents (admin): the plaintext appears
// exactly once, at mint time. A token's purpose has no default: a command-line
// token acts as its person; an agent token is bounded like an OAuth
// connection and is the only kind MCP accepts.
const tokenPurposes: Record<string, string> = {
  cli: 'command line: acts as its person',
  agent: 'coding agent: may not approve, land or administer',
}
export function TokensCard() {
  const tokensQ = useGet<TokenList>('/tokens')
  const usersQ = useGet<UserList>('/users')
  const tokens = tokensQ.data?.tokens ?? []
  const activeTokens = tokens.filter(t => !t.RevokedAt)
  const names = (usersQ.data?.users ?? []).map(u => u.username)
  const [name, setName] = useState('')
  const [actsAs, setActsAs] = useState('')
  const [role, setRole] = useState('')
  const [purpose, setPurpose] = useState('')
  const [minted, setMinted] = useState('')
  const { run, err } = useAction()
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Access tokens</b>
      <div className="muted small">
        <details><summary>About tokens</summary>
        A command-line token is for your own <code>apptown</code> CLI (set <code>APPTOWN_TOKEN</code>).
        An agent token is for a coding agent's MCP connection. A token is shown once.
        </details>
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={[tokensQ, usersQ]} what="the tokens" className="small" />
      <StaleNote q={[tokensQ, usersQ]} />
      {minted && (
        <div className="card tone-ok">
          <div className="small">Copy it now · it is not retrievable again:</div>
          <code style={{ userSelect: 'all', wordBreak: 'break-all' }}>{minted}</code>
        </div>
      )}
      {tokensQ.data && activeTokens.length === 0 && <p className="muted small">No access tokens yet.</p>}
      {activeTokens.map(t => (
        <div key={t.Name} className="row small">
          <span className="badge">{t.Name}</span>
          <span className="muted">{t.User} · {t.Role} · {tokenPurposes[t.Purpose] ?? 'no purpose'} · last used {t.LastUsed ? fmtWhen(t.LastUsed) : 'never'}</span>
          <span style={{ flex: 1 }} />
          <QuietConfirm label="revoke" ask={`Revoke token ${t.Name}? Anything using it stops working.`}
            onYes={() => { void run(() => del(`/tokens/${t.Name}`)) }} />
        </div>
      ))}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <input placeholder="token name (e.g. laptop-cli)" value={name} onChange={e => setName(e.target.value)} />
        <select value={actsAs} onChange={e => setActsAs(e.target.value)} title="whose name the token's actions are recorded under">
          <option value="">acts as you</option>
          {names.map(n => <option key={n} value={n}>acts as {n}</option>)}
        </select>
        <select value={role} onChange={e => setRole(e.target.value)} title="the most the token may do: a command-line token takes its person's own role unless narrowed; an agent token is a member's">
          <option value="">their own role</option>
          <option value="member">member</option>
          <option value="admin">admin</option>
        </select>
        <select value={purpose} onChange={e => setPurpose(e.target.value)} title="who will hold the token">
          <option value="">for…</option>
          {Object.entries(tokenPurposes).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
        </select>
        <button className="primary" disabled={!name || !purpose} onClick={async () => {
          const r = await run(() => post<IssuedToken>('/tokens', { name, purpose, ...(role ? { role } : {}), ...(actsAs ? { user: actsAs } : {}) }))
          if (r) { setMinted(r.token); setName(''); setPurpose('') }
        }}>Mint token</button>
      </div>
    </div>
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
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Roles in {PRODUCT}</b>
      <div className="muted small">
        Roles grant permissions. Project roles are under Settings › Members.
      </div>
      <details className="small"><summary>About permissions</summary>
        <p className="muted small">Instance administrators act in every project and may approve their own changes. Each project can require other people to get an independent approval.</p>
        {roles.filter(r => instance.some(g => g.role === r.name)).map(r => <div key={r.name}>
          <strong>{r.title}</strong><p>{r.description}</p><ul>{r.capabilities.map(c => <li key={c}><code>{c}</code></li>)}</ul>
        </div>)}
        {instance.filter(g => !g.role && g.capability).map(g => <p key={g.id}>{subjectLabel(g)}: <code>{g.capability}</code></p>)}
      </details>
      {byRole.size === 0 && <p className="muted small">No roles assigned.</p>}
      {[...byRole.entries()].map(([title, gs]) => (
        <div key={title} style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
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
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <select value={subject} onChange={e => setSubject(e.target.value)}>
            <option value="">who…</option>
            {grantees.map(g => <option key={g} value={g}>{g.startsWith('group:') ? groupLabel(g.slice(6)) : g}</option>)}
          </select>
          <select value={role} onChange={e => setRole(e.target.value)}>
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
  const accountTable = (list: Account[]) => <table className="people-accounts">
    <thead><tr><th>Name</th><th>Role</th><th>Last seen</th><th /></tr></thead>
    <tbody>{list.map(u => <tr key={u.username}>
      <td><strong>{u.username}</strong>{u.disabled && <> <span className="badge">Disabled</span></>}</td>
      <td>{u.role === 'admin' ? 'Admin' : 'Member'}</td>
      <td className="muted small">{u.lastSeen ? `seen ${fmtStamp(u.lastSeen)}` : 'never signed in'}</td>
      <td>{canEdit && <details className="account-actions"><summary aria-label={`Actions for ${u.username}`}>…</summary>
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
      </details>}</td>
    </tr>)}</tbody>
  </table>
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Accounts</b>
      {canEdit && sso && (
        <div className="muted small">
          Reset passwords through your sign-in provider.
        </div>
      )}
      {reset.err && <div className="error small">{reset.err}</div>}
      {resetLink && (
        <OneTimeLink made={resetLink} onDone={() => setResetLink(null)}>
          Password reset link for <b>{resetLink.invite.username}</b>. Copy it now · it is not shown again.
          Their current password and sessions keep working until they use it. Using it signs them out of browsers;
          their personal access tokens and agent connections keep working · revoke those on the People page if the account may be compromised:
        </OneTimeLink>
      )}
      {accounts.filter(u => !u.disabled).length === 0 && <span className="muted small">No active accounts.</span>}
      {accountTable(accounts.filter(u => !u.disabled))}
      {canEdit && invites && <InvitePerson />}
      {canEdit && (() => {
        const withPassword = (
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            <input placeholder="new username" value={name} onChange={e => setName(e.target.value)} />
            <input placeholder="password (min 8 chars)" type="password" value={pw} onChange={e => setPw(e.target.value)} />
            <button className={invites ? '' : 'primary'} disabled={!name || pw.length < 8}
              onClick={() => act(() => post('/users', { username: name, password: pw }).then(() => { setName(''); setPw('') }))}>
              Add person
            </button>
          </div>
        )
        // With invites, choosing someone's password is the exception: the
        // person sets their own from the link.
        return invites ? (
          <details>
            <summary className="muted small">Set their password instead</summary>
            <div className="muted small" style={{ margin: '6px 0' }}>
              You will have to pass the password on to them yourself.
            </div>
            {withPassword}
          </details>
        ) : withPassword
      })()}
      {(seen.length > 0 || accounts.some(u => u.disabled)) && (
        <details>
          <summary className="muted small">
            Other accounts and names ({seen.length + accounts.filter(u => u.disabled).length})
          </summary>
          {accounts.some(u => u.disabled) && <><h3>Disabled accounts</h3>{accountTable(accounts.filter(u => u.disabled))}</>}
          {seen.length > 0 && <h3>Names without an account ({seen.length})</h3>}
          <div className="muted small" style={{ margin: '6px 0' }}>
            Names that never signed in or acted. Set a password to claim one
            as a real account; remove the rest.
          </div>
          {seen.map(u => (
            <div key={u.username} className="row small" style={{ padding: '2px 0' }}>
              <span>{u.username}</span>
              {u.role === 'admin' && <span className="badge">admin</span>}
              <span className="muted">last seen {u.lastSeen ? fmtWhen(u.lastSeen) : '—'}</span>
              <span style={{ flex: 1 }} />
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
    </div>
  )
}

// Inviting a person: an administrator names the account and its role, and
// gets a link to pass on. The person opens it, sets their own password and
// is signed in. The link is shown once, here; AppTown sends no email.
export function InvitePerson() {
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
    <div style={{ display: 'grid', gap: 8 }}>
      <b className="small">Invite a person</b>
      <div className="muted small">
        <details><summary>About invites</summary>
        {PRODUCT} makes a link for one account. Send it to the person yourself;
        they choose their own password and are signed in. It works once,
        for seven days.
        </details>
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the invites waiting" className="small" />
      <StaleNote q={q} />
      {made && (
        <OneTimeLink made={made} onDone={() => setMade(null)}>
          Invite for <b>{made.invite.username}</b> ({made.invite.role}). Copy the link now · it is not shown again:
        </OneTimeLink>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <input placeholder="username (e.g. kim)" value={name} onChange={e => setName(e.target.value)} />
        <input placeholder="name or email (optional)" value={label} onChange={e => setLabel(e.target.value)}
          title={`a label for you; ${PRODUCT} sends no email`} />
        <select value={role} onChange={e => setRole(e.target.value)}>
          <option value="member">member</option>
          <option value="admin">admin</option>
        </select>
        <button className="primary" disabled={!name.trim() || busy} onClick={() => { void create() }}>Create invite link</button>
      </div>
      {pending.length > 0 && (
        <div style={{ display: 'grid', gap: 4 }}>
          <div className="muted small">Waiting to be used</div>
          {pending.map(i => (
            <div key={i.id} className="row small" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
              <span className="badge">{i.username}</span>
              {i.label && <span>{i.label}</span>}
              <span className="muted">
                {i.purpose === 'password' ? `password reset · made by ${i.invitedBy}` : `${i.role} · invited by ${i.invitedBy}`} · expires {fmtWhen(i.expiresAt)}
              </span>
              <span style={{ flex: 1 }} />
              <QuietConfirm label="withdraw" ask={`Withdraw the ${i.purpose === 'password' ? 'password reset link' : 'invite'} for ${i.username}? The link stops working.`}
                onYes={() => { void run(() => del(`/invites/${i.id}`)) }} />
            </div>
          ))}
        </div>
      )}
    </div>
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
    <div className="card tone-ok" style={{ display: 'grid', gap: 6 }}>
      <div className="small">{children}</div>
      <code style={{ userSelect: 'all', wordBreak: 'break-all' }}>{made.link}</code>
      <div className="row small" style={{ gap: 8 }}>
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
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Groups</b>
      {groups.length === 0 && <div className="muted small">No groups yet.</div>}
      {groups.filter(g => g.kind === 'built-in' || g.kind === 'personal').map(g => (
        <div key={g.name} className="row small" style={{ gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="badge">{groupLabel(g.name)}</span>
          <span className="muted">
            {g.kind === 'built-in' ? 'everyone who is signed in; grant this group a role to give everyone that access'
              : 'Personal group'}
          </span>
        </div>
      ))}
      {groups.filter(g => g.kind !== 'built-in' && g.kind !== 'personal').map(g => (
        <div key={g.name} style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="badge">{groupLabel(g.name)}</span>
          {canEdit ? users.map(u => {
            const inG = g.members.includes(u.username)
            return (
              <label key={u.username} className="small" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                <input type="checkbox" checked={inG}
                  onChange={() => act(() => post('/groups', {
                    name: g.name, description: g.description,
                    members: inG ? g.members.filter(m => m !== u.username) : [...g.members, u.username],
                  }))} />
                {u.username}
              </label>
            )
          }) : <span className="muted small">{g.members.join(', ') || 'no members'}</span>}
          <span style={{ flex: 1 }} />
          {canEdit && (
            <QuietConfirm label="delete" ask={`Delete group ${g.name}? Approval steps naming group:${g.name} will stop matching anyone.`}
              onYes={() => act(() => del(`/groups/${g.name}`))} />
          )}
        </div>
      ))}
      {canEdit && <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <input placeholder="new group name (e.g. reviewers)" value={name} onChange={e => setName(e.target.value)} />
        <button disabled={!name}
          onClick={() => act(() => post('/groups', { name, members: [] }).then(() => setName('')))}>
          Add group
        </button>
      </div>}
    </div>
  )
}
