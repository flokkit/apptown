import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { del, put } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus } from '../../ReadGate'
import type { InstanceSecret, SecretNameList } from '../../wire.gen'

// Instance secrets: the credentials the forge itself uses (a target's API
// token, an SSH key, a registry password), named by targets and
// connectors. Setting one is write-only (PUT /secrets/{name}): the value
// goes to the forge's secrets file and is never shown again. A name held in
// Infisical is changed there; the forge refuses to shadow it.

const NAME = /^[A-Z][A-Z0-9_]*$/

export function storeWords(store: string | undefined) {
  return store === 'secrets.env' ? `${PRODUCT}'s secrets file` : store === 'infisical' ? 'Infisical' : store ?? ''
}

// SetSecretNow: a password field that sets one named secret. Closed, it is
// a link-sized button; after a set it says the forge holds the name.
export function SetSecretNow({ name, held, label }: { name: string; held?: boolean; label?: string }) {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState('')
  const [done, setDone] = useState(false)
  const { run, busy, err } = useAction()
  const valid = NAME.test(name)
  if (!open) {
    return (
      <span className="row" style={{ gap: 6 }}>
        {done && <span className="small">✓ {PRODUCT} holds {name}</span>}
        <button type="button" className="subtle" disabled={!valid} title={valid ? '' : 'a secret name is upper case letters, digits and _'}
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

// SecretNameStatus: whether the forge holds a named secret, where, and the
// door to set it — for a form field that names one.
export function SecretNameStatus({ name }: { name: string }) {
  const namesQ = useGet<SecretNameList>('/secrets/names')
  const n = name.trim()
  if (!n) return null
  const store = namesQ.data?.stores?.[n]
  return (
    <div className="row small" style={{ gap: 8, flexWrap: 'wrap' }}>
      <span className={`dot ${namesQ.err || !namesQ.data ? 'unknown' : store ? 'ok' : 'bad'}`} />
      <code>{n}</code>
      {namesQ.err ? <span className="error">unknown — the secrets could not be read: {namesQ.reason}</span>
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

// InstanceSecretsPanel: every secret the forge holds, by name and where;
// set a new one, replace or remove one in the forge's secrets file.
export function InstanceSecretsPanel() {
  const namesQ = useGet<SecretNameList>('/secrets/names')
  const [name, setName] = useState('')
  const { run, err: actErr } = useAction()
  // A failed read is unknown, never "holds none": a list kept from before
  // the failure is not shown as current, and nothing is offered to set.
  const known = !!namesQ.data && !namesQ.err
  const names = namesQ.data?.names ?? []
  const stores = namesQ.data?.stores ?? {}
  const typed = name.trim().toUpperCase()
  return (
    <div style={{ maxWidth: 760 }}>
      <div className="muted small" style={{ marginBottom: 12 }}>
        Targets and connectors name the secret that holds their credential. Values are written here and never shown again;
        each set and removal is on the record with who did it. A secret held in Infisical is changed in Infisical.
      </div>
      {/* Here a failed refresh hides the list too, rather than marking it stale. */}
      {namesQ.err && <div className="error small">Unknown: {PRODUCT}'s secrets could not be read. {namesQ.reason}</div>}
      {!namesQ.err && <ReadStatus q={namesQ} what={`${PRODUCT}'s secrets`} className="small" />}
      {known && (
        <div style={{ borderTop: '1px solid var(--rule)' }}>
          {names.length === 0 && <div className="muted small" style={{ padding: '10px 0' }}>{PRODUCT} holds no secrets yet.</div>}
          {names.map((n) => (
            <div key={n} className="row" style={{ justifyContent: 'space-between', flexWrap: 'wrap', gap: 8, padding: '10px 0', borderBottom: '1px solid var(--rule-faint)' }}>
              <div style={{ minWidth: 0 }}>
                <code style={{ overflowWrap: 'anywhere' }}>{n}</code>
                <div className="muted small">held in {storeWords(stores[n])}</div>
              </div>
              {stores[n] === 'secrets.env'
                ? <span className="row small" style={{ gap: 6, flexWrap: 'wrap' }}>
                    <SetSecretNow name={n} held />
                    <QuietConfirm label="remove" ask={<>Remove {n}? Anything that names it will no longer find it.</>}
                      onYes={() => run(() => del<InstanceSecret>(`/secrets/${n}`))} />
                  </span>
                : <span className="muted small">change it in Infisical</span>}
            </div>
          ))}
        </div>
      )}
      {actErr && <div className="error small">{actErr}</div>}
      {known && <>
      <h3 style={{ marginTop: 20 }}>Set a secret</h3>
      <div className="row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="NAME_OF_THE_SECRET" aria-label="secret name"
          style={{ width: 240, maxWidth: '100%' }} />
        {typed && !NAME.test(typed) && <span className="error small">upper case letters, digits and _, starting with a letter</span>}
        {typed && NAME.test(typed) && (stores[typed] === 'infisical'
          ? <span className="muted small">{typed} is held in Infisical; change it there</span>
          : <SetSecretNow key={typed} name={typed} held={!!stores[typed]} label={stores[typed] ? 'replace its value' : 'set its value'} />)}
      </div>
      </>}
    </div>
  )
}
