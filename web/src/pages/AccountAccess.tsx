import { useState } from 'react'
import { del, post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { QuietConfirm } from '../QuietConfirm'
import { fmtStamp, fmtWhen } from '../format'
import type { AuthMe, DelegationList, IssuedToken, TokenList } from '../wire.gen'
import './People.css'

// Coding agents people have connected through the browser sign-in
// (Docs → Connect a coding agent). Yours, or everyone's for an admin.
// Disconnecting ends the agent's credential at once.
export function AgentsCard({ canEdit }: { canEdit: boolean }) {
  const q = useGet<DelegationList>(canEdit ? '/auth/delegations?all=1' : '/auth/delegations')
  const list = q.data?.delegations ?? []
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
          <div className="host-row"><strong>{newest.clientName}{connections.length > 1 ? ` ×${connections.length}` : ''} as {newest.user}</strong><span>Connected</span><span className="muted small">Latest connection {fmtStamp(newest.createdAt)}</span></div>
          <details><summary>Connections and permissions</summary>
      {connections.map(d => (
        <div key={d.id} className="row small people-div-2">
          <span className="badge">{d.clientName}</span>
          <span>as <b>{d.user}</b></span>
          <span className="muted">· {d.scopes.includes('apptown:user:read') && d.scopes.length === 1 ? 'read only' : d.scopes.includes('apptown:change:create') && d.scopes.includes('apptown:change:write') ? 'Can create and edit changes' : 'Connected access'}</span>
          <span className="muted">Connected {fmtStamp(d.createdAt)}{d.lastUsed ? `, last used ${fmtStamp(d.lastUsed)}` : ''}</span><details><summary>Permissions</summary><p>{d.scopes.join(', ')}</p></details>
          <span className="grow" />
          <QuietConfirm label="disconnect" ask={`Disconnect ${d.clientName} (as ${d.user})? It stops working at once; they can sign in again.`}
            onYes={() => { void run(() => del(`/auth/delegations/${d.id}`)) }} />
        </div>
      ))}</details></div>
      })}
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
    <Section title="Access tokens" count={personal ? meQ.data?.ownTokens?.length : tokensQ.data?.tokens.length} note={<>A command-line token is for your own <code>apptown</code> CLI (set <code>APPTOWN_TOKEN</code>). An agent token is for a coding agent's MCP connection. A token is shown once.</>}>
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
      {activeTokens.map(t => (
        <div key={t.Name} className="row small">
          <span className="badge">{t.Name}</span>
          <span className="muted">{t.User} · {t.Role} · {tokenPurposes[t.Purpose] ?? 'no purpose'} · last used {t.LastUsed ? fmtWhen(t.LastUsed) : 'never'}</span>
          <span className="grow" />
          <QuietConfirm label="revoke" ask={`Revoke token ${t.Name}? Anything using it stops working.`}
            onYes={() => { void run(() => del(`${personal ? '/account/tokens' : '/tokens'}/${t.Name}`)) }} />
        </div>
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

