import { PRODUCT } from '../brand'
import { useEffect, useState } from 'react'
import { HttpError, del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate, ReadStatus, StaleNote } from '../ReadGate'
import { QuietConfirm, QuietPrompt } from '../QuietConfirm'

import type { AuthMe, Account, AdministratorNetworksView, DelegationList, GrantList, GrantView, Group, GroupList, InviteLink, InviteList, IssuedToken, RoleCatalog, RoleView, TokenList, UserList } from '../wire.gen'
import { useHolds } from '../instancePages'
import { grantTitle, subjectLabel } from '../parts/grants'

// The server's shapes, generated from its Go types (wire.gen.ts).
export type Role = RoleView

// People: who can act here, and the groups that stand in for them in
// approval steps. An approval step naming "group:reviewers" is satisfied by
// any member — the step cites the group, the ledger records the person.
export default function People() {
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
      <div>
        <h1 style={{ margin: 0 }}>People</h1>
        <div className="muted small">
          Accounts on this instance, and groups that can be named as approvers
          (write <code>group:name</code> in a workflow's approver list — any
          member's approval counts, and the record keeps the person's name).
        </div>
      </div>
      {err && <div className="card error">{err}</div>}

      {!canEdit && !canUsers && !canGroups && !canGrants && (
        <div className="muted small">
          Only an admin can change these settings.
        </div>
      )}
      <ReadGate q={usersQ} what="the accounts">
        <UsersCard users={users} act={act} canEdit={canUsers} invites={meQ.data?.mode === 'internal'} sso={!!meQ.data && meQ.data.mode !== 'internal'} me={meQ.data?.user ?? ''} />
      </ReadGate>
      <ReadGate q={groupsQ} also={[usersQ]} what="the groups">
        <GroupsCard groups={groups} users={users} act={act} canEdit={canGroups} />
      </ReadGate>
      <ReadGate q={rolesQ} also={[grantsQ, usersQ, groupsQ]} what="the roles and grants">
        <RolesCard roles={roles} grants={grants} users={users} groups={groups} act={act} canEdit={canGrants} />
      </ReadGate>
      <AgentsCard canEdit={canEdit} />
      <AdminNetworksCard canEdit={canEdit} />
      {canEdit && <TokensCard />}
    </div>
  )
}


// Coding agents people have connected through the browser sign-in
// (Docs → Connect a coding agent). Yours, or everyone's for an admin.
// Disconnecting ends the agent's credential at once.
function AgentsCard({ canEdit }: { canEdit: boolean }) {
  const q = useGet<DelegationList>(canEdit ? '/auth/delegations?all=1' : '/auth/delegations')
  const list = q.data?.delegations ?? []
  const { run, err } = useAction()
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Connected coding agents</b>
      <div className="muted small">
        Coding agents that signed in through the browser. Each acts as the
        person who connected it, within what they allowed.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the connected agents" className="small" />
      <StaleNote q={q} />
      {q.data && list.length === 0 && <span className="muted small">none yet — see Docs → Connect a coding agent</span>}
      {list.map(d => (
        <div key={d.id} className="row small" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="chip">{d.clientName}</span>
          <span>as <b>{d.user}</b></span>
          <span className="muted">· {d.scopes.includes('apptown:user:read') && d.scopes.length === 1 ? 'read only' : d.scopes.join(', ')}</span>
          <span className="muted">· connected {d.createdAt.slice(0, 10)}{d.lastUsed ? `, last used ${d.lastUsed.slice(0, 10)}` : ''}</span>
          <span style={{ flex: 1 }} />
          <QuietConfirm label="disconnect" ask={`Disconnect ${d.clientName} (as ${d.user})? It stops working at once; they can sign in again.`}
            onYes={() => { void run(() => del(`/auth/delegations/${d.id}`)) }} />
        </div>
      ))}
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
        An administrator account can sign in, connect a coding agent, or use an
        administrator token only from these networks. Members are not limited.
        Empty means anywhere; the machine {PRODUCT} runs on always passes.
        You are connecting from <code>{you || '…'}</code>{nets.length > 0 && (inside ? ', which is inside' : ', which is outside the list')}.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the administrator networks" className="small" />
      <StaleNote q={q} />
      {q.data && <div className="row small" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        {nets.length === 0 && <span className="muted">anywhere (no networks listed)</span>}
        {nets.map((n) => (
          <span key={n} className="chip">{n}{editable && <>{' '}
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
  const names = (usersQ.data?.users ?? []).map(u => u.username)
  const [name, setName] = useState('')
  const [actsAs, setActsAs] = useState('')
  const [role, setRole] = useState('')
  const [purpose, setPurpose] = useState('')
  const [minted, setMinted] = useState('')
  const { run, err } = useAction()
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>API tokens</b>
      <div className="muted small">
        A command-line token is for your own <code>apptown</code> CLI (set <code>APPTOWN_TOKEN</code>).
        An agent token is for a coding agent's MCP connection. A token is shown once.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={[tokensQ, usersQ]} what="the tokens" className="small" />
      <StaleNote q={[tokensQ, usersQ]} />
      {minted && (
        <div className="card tone-ok">
          <div className="small">Copy it now — it is not retrievable again:</div>
          <code style={{ userSelect: 'all', wordBreak: 'break-all' }}>{minted}</code>
        </div>
      )}
      {tokens.filter(t => !t.RevokedAt).map(t => (
        <div key={t.Name} className="row small">
          <span className="chip">{t.Name}</span>
          <span className="muted">{t.User} · {t.Role} · {tokenPurposes[t.Purpose] ?? 'no purpose'} · last used {t.LastUsed ? t.LastUsed.slice(0, 10) : 'never'}</span>
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
  const grantees = [...users.map(u => u.username), ...groups.map(g => `group:${g.name}`)]
  const instance = grants.filter(g => g.scopeType === 'instance' || g.scopeType === 'release-target')
  const offered = roles.filter(r => r.surfaced && r.scope === 'instance')
  const byRole = new Map<string, GrantView[]>()
  for (const g of instance) {
    const k = grantTitle(g)
    byRole.set(k, [...(byRole.get(k) || []), g])
  }
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Roles in {PRODUCT}</b>
      <div className="muted small">
        A role is a named bundle of things a person may do. Nothing is
        implied: if it is not on this list or a project's Members tab, nobody
        holds it. Who can do what on one project is on that project's
        Members tab.
      </div>
      <p className="muted small">Instance administrators act in every project and may approve their own changes. Each project can require other people to get an independent approval.</p>
      {[...byRole.entries()].map(([title, gs]) => (
        <div key={title} style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="chip" title={roles.find(r => r.title === title)?.description}>{title}</span>
          {gs.map(g => (
            <span key={g.id} className="chip" title={[g.reason, g.origin && `origin: ${g.origin}`, `granted by ${g.grantedBy}`].filter(Boolean).join(' · ')}>
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
            {grantees.map(g => <option key={g} value={g}>{g}</option>)}
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
  return (
    <div className="card" style={{ display: 'grid', gap: 10 }}>
      <b>Accounts</b>
      {canEdit && sso && (
        <div className="muted small">
          Password reset links need {PRODUCT}'s own sign-in (--auth internal); with single sign-on, reset the password in your identity provider.
        </div>
      )}
      {reset.err && <div className="error small">{reset.err}</div>}
      {resetLink && (
        <OneTimeLink made={resetLink} onDone={() => setResetLink(null)}>
          Password reset link for <b>{resetLink.invite.username}</b>. Copy it now — it is not shown again.
          Their current password and sessions keep working until they use it. Using it signs them out of browsers;
          their personal access tokens and agent connections keep working — revoke those on the People page if the account may be compromised:
        </OneTimeLink>
      )}
      <table className="stack">
        <thead><tr><th>User</th><th>Role</th><th>Password</th><th>Groups</th><th>Last seen</th><th /></tr></thead>
        <tbody>
          {accounts.map(u => (
            <tr key={u.username}>
              <td className="stack-lead"><strong>{u.username}</strong>{u.disabled && <> <span className="chip fail">disabled</span></>}</td>
              <td>
                {canEdit ? (
                  <select value={u.role}
                    onChange={e => act(() => post('/users', { username: u.username, role: e.target.value }))}>
                    <option value="member">member</option>
                    <option value="admin">admin</option>
                  </select>
                ) : <span className="chip">{u.role}</span>}
              </td>
              <td className="small muted" data-label="password">{u.hasPassword ? 'set' : '—'}</td>
              <td className="small muted" data-label="groups">{u.groups.join(', ') || '—'}</td>
              <td className="small muted" data-label="last seen">{u.lastSeen ? u.lastSeen.slice(0, 10) : '—'}</td>
              <td>
                {/* A reset link lets the person choose the new password
                    themselves; on use every session of theirs ends. Not for a
                    disabled account: enable it first. */}
                {canEdit && invites && !u.disabled && (
                  <QuietConfirm label="reset password" className="subtle" yes="Make link"
                    ask={`Make a password reset link for ${u.username}? You give it to them; when they use it, their old password stops working. This signs them out of browsers. Their personal access tokens and agent connections keep working — revoke those on the People page if the account may be compromised.`}
                    onYes={async () => {
                      const r = await reset.run(() => post<InviteLink>('/password-links', { username: u.username }))
                      if (r) setResetLink(r)
                    }} />
                )}
                {canEdit && (
                  <QuietPrompt label="set password" yes="Set"
                    ask={`New password for ${u.username} — their current sessions will be signed out.`}
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
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
            <summary className="muted small">Add a person with a password you choose instead</summary>
            <div className="muted small" style={{ margin: '6px 0' }}>
              You will have to pass the password on to them yourself.
            </div>
            {withPassword}
          </details>
        ) : withPassword
      })()}
      {seen.length > 0 && (
        <details>
          <summary className="muted small">
            names without an account ({seen.length})
          </summary>
          <div className="muted small" style={{ margin: '6px 0' }}>
            Names that never signed in or acted. Set a password to claim one
            as a real account; remove the rest.
          </div>
          {seen.map(u => (
            <div key={u.username} className="row small" style={{ padding: '2px 0' }}>
              <span>{u.username}</span>
              {u.role === 'admin' && <span className="chip">admin</span>}
              <span className="muted">last seen {u.lastSeen ? u.lastSeen.slice(0, 10) : '—'}</span>
              <span style={{ flex: 1 }} />
              {canEdit && <>
                <QuietPrompt label="claim" yes="Claim"
                  ask={`Set a password for ${u.username} — this makes it a sign-in account.`}
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
        {PRODUCT} makes a link for one account. Send it to the person yourself;
        they choose their own password and are signed in. It works once,
        for seven days.
      </div>
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the invites waiting" className="small" />
      <StaleNote q={q} />
      {made && (
        <OneTimeLink made={made} onDone={() => setMade(null)}>
          Invite for <b>{made.invite.username}</b> ({made.invite.role}). Copy the link now — it is not shown again:
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
              <span className="chip">{i.username}</span>
              {i.label && <span>{i.label}</span>}
              <span className="muted">
                {i.purpose === 'password' ? `password reset · made by ${i.invitedBy}` : `${i.role} · invited by ${i.invitedBy}`} · expires {i.expiresAt.slice(0, 10)}
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
      {groups.length === 0 && <div className="muted small">No groups yet. Name a group (say, <code>reviewers</code>) in an approval step's approver list — any member's OK satisfies it.</div>}
      {groups.filter(g => g.kind === 'built-in' || g.kind === 'personal').map(g => (
        <div key={g.name} className="row small" style={{ gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="chip">{g.name}</span>
          <span className="muted">
            {g.kind === 'built-in' ? 'everyone who is signed in; grant this group a role to give everyone that access'
              : `${g.members.join(', ') || g.name}'s own group; it owns the projects they create`}
          </span>
        </div>
      ))}
      {groups.filter(g => g.kind !== 'built-in' && g.kind !== 'personal').map(g => (
        <div key={g.name} style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="chip">{g.name}</span>
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
