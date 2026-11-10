import { Tech } from '../Tech'
import { ListRow } from '../ListRow'
import { useState } from 'react'
import { del, post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { QuietConfirm } from '../QuietConfirm'
import { Modal } from '../Modal'
import { ListTable } from '../ListTable'
import { fmtStamp, fmtWhen, relTime } from '../format'
import type { AuthMe, DelegationList, IssuedToken, TokenList } from '../wire.gen'
import './People.css'

// A credential's consequence is asked in a sized dialog: the table's action
// cell has no room for the question, and a box opened under the button would
// cover the very row it is about.
function CredentialConfirm({ label, ask, onYes }: { label: string; ask: string; onYes: () => void }) {
  const [asking, setAsking] = useState(false)
  return <>
    <button className="subtle danger" onClick={() => setAsking(true)}>{label}</button>
    {asking && <Modal fit title={label[0].toUpperCase() + label.slice(1)} onClose={() => setAsking(false)} actions={<>
      <button className="danger" onClick={() => { onYes(); setAsking(false) }}>Yes</button>
      <button onClick={() => setAsking(false)}>No</button>
    </>}>
      <p>{ask}</p>
    </Modal>}
  </>
}

// Coding agents people have connected through the browser sign-in
// (Docs → Connect a coding agent). Yours, or everyone's for an admin.
// Disconnecting ends the agent's credential at once.
export function AgentsCard({ canEdit }: { canEdit: boolean }) {
  const q = useGet<DelegationList>(canEdit ? '/auth/delegations?all=1' : '/auth/delegations')
  const list = q.data?.delegations ?? []
  const [selected, setSelected] = useState<typeof list[number] | null>(null)
  const { run, err } = useAction()
  return (
    <Section title="Connected coding agents" note="Coding agents that signed in through the browser. Each acts as the person who connected it, within what they allowed.">
    <div className="people-agents-card-div">
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={q} what="the connected agents" className="small" />
      <StaleNote q={q} />
      {q.data && list.length === 0 && <span className="muted small">None yet.</span>}
      {[...new Set(list.map(d => `${d.clientName}\0${d.user}`))].map(key => {
        const connections = list.filter(d => `${d.clientName}\0${d.user}` === key).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
        const newest = connections[0]
        return <div key={key} className="agent-group">
          <div className="host-row"><strong>{newest.clientName}{connections.length > 1 ? ` ×${connections.length}` : ''} as {newest.user}</strong><span>Connected</span><span className="muted small">Latest connection {fmtStamp(newest.createdAt ?? '')}</span></div>
          {canEdit ? <ListTable label={`${newest.clientName} connections for ${newest.user}`} rows={connections} rowKey={d => String(d.id)} columns={[
            { key: 'client', label: 'Agent', phone: 'name', render: d => d.clientName },
            { key: 'user', label: 'Account', phone: 'state', render: d => d.user },
            { key: 'permissions', label: 'Permissions', render: d => <button className="link" aria-label={`Permissions for ${d.clientName} as ${d.user}`} onClick={() => setSelected(d)}>Access</button> },
            // Reconnecting registers a new connection: the time it was made is
            // what tells two of the same agent apart, on a phone too.
            { key: 'connected', label: 'Connected', render: d => fmtStamp(d.createdAt ?? '') || '—' },
            { key: 'used', label: 'Last used', kind: 'time', render: d => <span title={fmtStamp(d.lastUsed ?? '')}>{relTime(d.lastUsed ?? '') || 'never'}</span> },
            { key: 'action', label: '', phone: 'action', render: d => <CredentialConfirm label="disconnect" ask={`Disconnect ${d.clientName} (as ${d.user}, connected ${fmtStamp(d.createdAt ?? '')})? It stops working at once; they can sign in again.`} onYes={() => { void run(() => del(`/auth/delegations/${d.id}`)) }} /> },
          ]} /> : <section><h3>Connections and permissions</h3>
      <ListTable className="small" label={`${newest.clientName} connections and permissions for ${newest.user}`} rows={connections} rowKey={d => String(d.id)} columns={[
        { key: 'client', label: 'Agent', phone: 'name', render: d => <><span className="badge">{d.clientName}</span><span className="muted"> as </span><b>{d.user}</b></> },
        { key: 'permissions', label: 'Permissions', render: d => <><span>{d.scopes.includes('apptown:user:read') && d.scopes.length === 1 ? 'read only' : d.scopes.includes('apptown:change:create') && d.scopes.includes('apptown:change:write') ? 'Can create and edit changes' : 'Connected access'}</span><code className="tech-line muted"><Tech>{d.scopes.join(', ')}</Tech></code></> },
        { key: 'connected', label: 'Connected', render: d => fmtStamp(d.createdAt) },
        { key: 'used', label: 'Last used', kind: 'time', render: d => d.lastUsed ? fmtStamp(d.lastUsed) : '' },
        { key: 'action', label: '', phone: 'action', render: d => <QuietConfirm label="disconnect" ask={`Disconnect ${d.clientName} (as ${d.user}, connected ${fmtStamp(d.createdAt)})? It stops working at once; they can sign in again.`} onYes={() => { void run(() => del(`/auth/delegations/${d.id}`)) }} /> },
      ]} /></section>}
        </div>
      })}
      {selected && <Modal title={`${selected.clientName} as ${selected.user}`} onClose={() => setSelected(null)}>
        <p>Connected {fmtStamp(selected.createdAt)} · last used {selected.lastUsed ? fmtStamp(selected.lastUsed) : 'never'}</p>
        <p>{selected.scopes.includes('apptown:user:read') && selected.scopes.length === 1 ? 'Read only' : selected.scopes.includes('apptown:change:create') && selected.scopes.includes('apptown:change:write') ? 'Can create and edit changes' : 'Connected access'}</p>
        <ul>{selected.scopes.map(scope => <li key={scope}><code>{scope}</code></li>)}</ul>
      </Modal>}
    </div>
    </Section>
  )
}

// Account owns personal token creation; People shares the administrator inventory.
// The plaintext appears
// exactly once, at mint time. A token's purpose has no default: a command-line
// token acts as its person; an agent token is bounded like an OAuth
// connection and is the only kind MCP accepts.
const tokenPurposes: Record<string, string> = {
  cli: 'command line: acts as its person',
  agent: 'coding agent: may not approve, land or administer',
}
export function TokensCard({ personal = false }: { personal?: boolean }) {
  const tokensQ = useGet<TokenList>(personal ? null : '/tokens')
  const meQ = useGet<AuthMe>(personal ? '/auth/me' : null)
  const tokens = personal ? meQ.data?.ownTokens ?? [] : tokensQ.data?.tokens ?? []
  const activeTokens = tokens.filter(t => !t.RevokedAt)
  const [name, setName] = useState('')
  const [purpose, setPurpose] = useState('')
  const [minted, setMinted] = useState('')
  const { run, err } = useAction(undefined, { affects: personal ? ['/auth/me'] : [] })
  return (
    <Section title="Access tokens" count={(personal ? meQ.data : tokensQ.data) ? activeTokens.length : undefined} note={<>A command-line token is for your own <code>apptown</code> CLI (set <code>APPTOWN_TOKEN</code>). An agent token is for a coding agent's MCP connection. A token is shown once.</>}>
    <div className="people-agents-card-div">
      {err && <div className="error small">{err}</div>}
      <ReadStatus q={personal ? [meQ] : [tokensQ]} what="the tokens" className="small" />
      <StaleNote q={personal ? [meQ] : [tokensQ]} />
      {minted && (
        <div className="card tone-ok">
          <div className="small">Copy it now · it is not retrievable again:</div>
          <code className="people-tokens-card-code">{minted}</code>
        </div>
      )}
      {(personal ? meQ.data : tokensQ.data) && activeTokens.length === 0 && <p className="muted small">No access tokens yet.</p>}
      {!personal && <ListTable label="Access tokens" rows={activeTokens} rowKey={t => t.Name} columns={[
        { key: 'name', label: 'Name', phone: 'name', kind: 'technical', render: t => t.Name },
        { key: 'user', label: 'Account', phone: 'state', render: t => t.User },
        { key: 'role', label: 'Role', render: t => t.Role },
        { key: 'purpose', label: 'Purpose', render: t => <span title={tokenPurposes[t.Purpose] ?? 'no purpose'}>{t.Purpose === 'cli' ? 'CLI' : t.Purpose === 'agent' ? 'Agent' : 'No purpose'}</span> },
        { key: 'used', label: 'Last used', kind: 'time', render: t => <span title={fmtStamp(t.LastUsed ?? '')}>{relTime(t.LastUsed ?? '') || 'never'}</span> },
        { key: 'action', label: '', phone: 'action', render: t => <CredentialConfirm label="revoke" ask={`Revoke token ${t.Name}? Anything using it stops working.`} onYes={() => { void run(() => del(`${personal ? '/account/tokens' : '/tokens'}/${t.Name}`)) }} /> },
      ]} />}
      {personal && activeTokens.map(t => (
        <ListRow key={t.Name} className="row small personal-token-row" identity={<>
          <span className="badge">{t.Name}</span>
          <span className="muted">{t.User} · {t.Role} · {tokenPurposes[t.Purpose] ?? 'no purpose'} · last used {t.LastUsed ? fmtWhen(t.LastUsed) : 'never'}</span>
          </>} action={<QuietConfirm label="revoke" ask={`Revoke token ${t.Name}? Anything using it stops working.`}
            onYes={() => { void run(() => del(`${personal ? '/account/tokens' : '/tokens'}/${t.Name}`)) }} />} />
      ))}
      {personal && <div className="people-form">
        <input aria-label="Token name" placeholder="token name (e.g. laptop-cli)" value={name} onChange={e => setName(e.target.value)} />
        <select aria-label="Token holder" value={purpose} onChange={e => setPurpose(e.target.value)} title="who will hold the token">
          <option value="">for…</option>
          {Object.entries(tokenPurposes).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
        </select>
        <button className="primary" disabled={!name || !purpose} onClick={async () => {
          const r = await run(() => post<IssuedToken>('/account/tokens', { name, purpose }))
          if (r) { setMinted(r.token); setName(''); setPurpose('') }
        }}>Mint token</button>
      </div>}
    </div>
    </Section>
  )
}

