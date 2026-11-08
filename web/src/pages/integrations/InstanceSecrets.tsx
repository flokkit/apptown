import { StatusDot } from '../../Status'
import { ListRow } from '../../ListRow'
import { Section } from '../../Section'
import { Link } from 'react-router-dom'
import { credentialKeys, specFor, areaOf } from './kinds'
import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { del, put } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus } from '../../ReadGate'
import type { ConnectorEntry, IntegrationEntry, InstanceSecret, SecretNameList } from '../../wire.gen'

// Instance secrets: the credentials AppTown itself uses (a target's API
// token, an SSH key, a registry password), named by targets and
// connectors. Setting one is write-only (PUT /secrets/{name}): the value
// goes to AppTown's secrets file and is never shown again. A name held in
// Infisical is changed there; AppTown refuses to shadow it.

const NAME = /^[A-Z][A-Z0-9_]*$/

export function storeWords(store: string | undefined) {
  return store === 'secrets.env' ? `${PRODUCT}'s secrets file` : store === 'infisical' ? 'Infisical' : store ?? ''
}

// SetSecretNow: a password field that sets one named secret. Closed, it is
// a link-sized button; after a set it says AppTown holds the name.
export function SetSecretNow({ name, held, label, className = 'subtle' }: { name: string; held?: boolean; label?: string; className?: string }) {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState('')
  const [done, setDone] = useState(false)
  const { run, busy, err } = useAction()
  const valid = NAME.test(name)
  if (!open) {
    return (
      <span className="row" style={{ gap: 6 }}>
        {done && <span className="small">✓ {PRODUCT} holds {name}</span>}
        <button type="button" className={className} disabled={!valid} title={valid ? '' : 'a secret name is upper case letters, digits and _'}
          onClick={() => { setOpen(true); setDone(false) }}>
          {label ?? (held || done ? 'replace its value' : 'set this secret now')}
        </button>
      </span>
    )
  }
  const save = async () => {
    const r = await run(() => put<InstanceSecret>(`/secrets/${name}`, { value }))
    if (r?.held) { setValue(''); setOpen(false); setDone(true) }
  }
  return (
    <span className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
      <input type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)}
        placeholder={`value of ${name}`} aria-label={`value of ${name}`} style={{ width: 240, maxWidth: '100%' }}
        onKeyDown={(e) => { if (e.key === 'Enter' && value.trim()) { e.preventDefault(); void save() } }} autoFocus />
      <button type="button" className="primary" disabled={busy || !value.trim()} onClick={save}>{busy ? 'setting…' : 'Set'}</button>
      <button type="button" className="subtle" disabled={busy} onClick={() => { setOpen(false); setValue('') }}>cancel</button>
      {err && <span className="error small">{err}</span>}
    </span>
  )
}

// SecretNameStatus: whether AppTown holds a named secret, where, and the
// door to set it — for a form field that names one.
export function SecretNameStatus({ name }: { name: string }) {
  const namesQ = useGet<SecretNameList>('/secrets/names')
  const n = name.trim()
  if (!n) return null
  const store = namesQ.data?.stores?.[n]
  return (
    <div className="row small" style={{ gap: 8, flexWrap: 'wrap' }}>
      <StatusDot word={namesQ.err || !namesQ.data ? 'unknown' : store ? 'current' : 'missing'} />
      <code>{n}</code>
      {namesQ.err ? <span className="error">unknown · the secrets could not be read: {namesQ.reason}</span>
        : !namesQ.data ? <span className="muted">checking…</span>
          : store ? <span className="muted">held in {storeWords(store)}</span>
            : <span className="muted">{PRODUCT} does not hold this yet</span>}
      {namesQ.err || !namesQ.data ? null
        : store === 'infisical'
          ? <span className="muted">change it in Infisical</span>
          : <SetSecretNow key={n} name={n} held={!!store} />}
    </div>
  )
}

// Uses only the integration configuration already read by the route.
export function InstanceSecretsPanel({ bindings = [], catalog = [] }: { bindings?: IntegrationEntry[]; catalog?: ConnectorEntry[] }) {
  const namesQ = useGet<SecretNameList>('/secrets/names')
  const [adding, setAdding] = useState(false)
  const { run, err: actErr } = useAction()
  // A failed read is unknown, never "holds none": a list kept from before
  // the failure is not shown as current, and nothing is offered to set.
  const known = !!namesQ.data && !namesQ.err
  const names = namesQ.data?.names ?? []
  const stores = namesQ.data?.stores ?? {}
  const places = new Set(names.map((n) => stores[n]))
  const mixed = places.size > 1
  const uses = names.map((name) => bindings.filter((b) => credentialKeys(b, specFor(catalog, b)).some((key) => {
    const ref = b.config[key]
    return Array.isArray(ref) ? ref.includes(name) : ref === name
  })))
  const showUses = uses.some((users) => users.length > 0)
  return (
    <Section title={`Credentials ${PRODUCT} uses`} action={known && <button onClick={() => setAdding(true)} disabled={adding}>Add a credential</button>}>
      {/* Here a failed refresh hides the list too, rather than marking it stale. */}
      {namesQ.err && <div className="error small">Unknown: {PRODUCT}'s secrets could not be read. {namesQ.reason}</div>}
      {!namesQ.err && <ReadStatus q={namesQ} what={`${PRODUCT}'s secrets`} className="small" />}
      {known && <>
        {names.length > 0 && !mixed && <div className="muted small">All stored in {storeWords(stores[names[0]])}</div>}
        <div role="list" className={`credential-list${mixed ? ' credential-stores' : ''}${showUses ? ' credential-uses' : ''}`}>
          {adding && <AddCredential stores={stores} onDone={() => setAdding(false)} />}
          {names.length === 0 && <div className="muted small" style={{ padding: '10px 0' }}>{PRODUCT} holds no secrets yet.</div>}
          {names.map((n, i) => <ListRow key={n} listItem className="credential-row"
            identity={<code>{n}</code>}
            state={<>
              {mixed && <span className="muted small">{storeWords(stores[n])}</span>}
              {showUses && <span className="small credential-users">{uses[i].length > 0 && <>Used by {uses[i].map((b, j) => <span key={b.name}>{j > 0 && ', '}<Link to={`/${areaOf(b.capability)}/${b.name}`}>{b.displayLabel || b.name}</Link></span>)}</>}</span>}
            </>}
            action={stores[n] === 'secrets.env' ? <>
              <SetSecretNow name={n} held label="Replace" className="" />
              <QuietConfirm label="Remove" className="danger" ask={<>Remove {n}? Anything that names it will no longer find it.</>}
                onYes={() => run(() => del<InstanceSecret>(`/secrets/${n}`))} />
            </> : stores[n] === 'infisical' ? <span className="muted small">change it in Infisical</span> : null} />)}
        </div>
      </>}
      {actErr && <div className="error small">{actErr}</div>}
    </Section>
  )
}

function AddCredential({ stores, onDone }: { stores: Record<string, string>; onDone: () => void }) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const { run, busy, err } = useAction()
  const typed = name.trim().toUpperCase()
  const infisical = stores[typed] === 'infisical'
  const valid = NAME.test(typed) && !!value.trim() && !infisical
  return <form role="listitem" className="credential-add row" onSubmit={async (e) => {
    e.preventDefault()
    if (!valid || busy) return
    const r = await run(() => put<InstanceSecret>(`/secrets/${typed}`, { value }))
    if (r?.held) onDone()
  }}>
    <input value={name} onChange={(e) => setName(e.target.value)} placeholder="NAME_OF_THE_SECRET" aria-label="secret name" autoFocus disabled={busy} />
    <input type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} placeholder="Value" aria-label="credential value" disabled={busy} />
    <button className="primary" disabled={busy || !valid}>{busy ? 'Saving…' : 'Save'}</button>
    <button type="button" disabled={busy} onClick={onDone}>Cancel</button>
    {typed && !NAME.test(typed) && <span className="error small">upper case letters, digits and _, starting with a letter</span>}
    {infisical && <span className="muted small">{typed} is held in Infisical; change it there</span>}
    {err && <span className="error small">{err}</span>}
  </form>
}
