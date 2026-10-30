import { useState } from 'react'
import { post } from '../../api'
import { ConnIcon } from '../../icons'
import { firstErr, useAction, useGet } from '../../data'
import type { ConnectorEntry, DiscoveredModels, IntegrationHealth, SecretNameList } from '../../wire.gen'
import { storeWords } from './InstanceSecrets'

// The secret selector: pick credential references from the names that
// actually resolve in the secret manager. Names only — values never reach
// the browser. Unknown keys can be added by hand and show as pending.
function SecretRefPicker({ value, onChange, help }: {
  value: string[]; onChange: (v: string[]) => void; help?: string
}) {
  const namesQ = useGet<SecretNameList>('/secrets/names')
  const [manual, setManual] = useState<string | null>(null) // non-null = "Other…" input open
  // When the store cannot be read, nothing is known to resolve or not: the
  // keys can still be entered by hand.
  const unknown = namesQ.err !== ''
  const available = namesQ.data ? namesQ.data.names : unknown ? [] : null

  if (available === null) return <span className="muted small">Reading the secret names…</span>
  const unselected = available.filter((n) => !value.includes(n))

  return (
    <span>
      {value.map((n) => (
        <div className="row" key={n} style={{ marginBottom: 6 }}>
          <span className={`dot ${unknown ? '' : available.includes(n) ? 'ok' : 'bad'}`}
            title={unknown ? 'unknown · the secret store could not be read' : available.includes(n) ? `held in ${storeWords(namesQ.data?.stores?.[n])}` : 'not in the secret store yet · add this key to it'} />
          <code>{n}</code>
          {!unknown && !available.includes(n) && <span className="muted small">not resolved yet</span>}
          <button className="subtle danger" onClick={() => onChange(value.filter((v) => v !== n))}>remove</button>
        </div>
      ))}
      {unknown && <div className="error small" style={{ marginBottom: 6 }}>The secret store could not be read: {namesQ.err}</div>}
      <div className="row">
        {manual === null ? (
          <select value="" onChange={(e) => {
            if (e.target.value === '__other__') setManual('')
            else if (e.target.value) onChange([...value, e.target.value])
          }}>
            <option value="">{value.length === 0 ? 'Select a secret…' : 'Add another secret…'}</option>
            {unselected.map((n) => <option key={n} value={n}>{n}</option>)}
            <option value="__other__">Other (not in the secret store yet)…</option>
          </select>
        ) : (
          <>
            <input value={manual} onChange={(e) => setManual(e.target.value)} placeholder="KEY_NAME" autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  const k = manual.trim()
                  if (k && !value.includes(k)) onChange([...value, k])
                  setManual(null)
                }
                if (e.key === 'Escape') setManual(null)
              }} style={{ width: 220 }} />
            <button className="subtle" onClick={() => {
              const k = manual.trim()
              if (k && !value.includes(k)) onChange([...value, k])
              setManual(null)
            }}>add</button>
            <button className="subtle" onClick={() => setManual(null)}>cancel</button>
          </>
        )}
      </div>
      {help && <div className="muted small" style={{ marginTop: 4 }}>{help}</div>}
    </span>
  )
}

// Curated model list for a provider binding: discovery pulls the live
// catalog (openrouter's public list, or the draft endpoint's /models) and a
// type-ahead adds ids; the curated list feeds slot selectors as patterns.
function ModelListPicker({ value, onChange, providerName, config, help }: {
  value: string[]; onChange: (v: string[]) => void
  providerName: string; config: Record<string, unknown>; help?: string
}) {
  const [available, setAvailable] = useState<string[] | null>(null)
  const [q, setQ] = useState('')
  const { run, err: derr } = useAction()

  const discover = () => void run(async () => {
    setAvailable((await post<DiscoveredModels>('/integrations/discover', { name: providerName, config })).models)
  })
  const toggle = (id: string) =>
    onChange(value.includes(id) ? value.filter((v) => v !== id) : [...value, id])

  // Enabled models float to the top; the rest filtered by search.
  const filtered = (available ?? []).filter((m) => m.toLowerCase().includes(q.toLowerCase()))
  const listed = available === null ? [] :
    [...value.filter((v) => filtered.includes(v) || q === ''), ...filtered.filter((m) => !value.includes(m))]

  return (
    <span>
      {value.length > 0 && (
        <div className="model-chips">
          {value.map((id) => (
            <span className="model-chip" key={id}>
              {id}
              <button title="disable" aria-label={`disable ${id}`} onClick={() => onChange(value.filter((v) => v !== id))}>×</button>
            </span>
          ))}
        </div>
      )}
      {available === null ? (
        <div className="row">
          <button className="subtle" onClick={discover}>
            {value.length === 0 ? 'Browse provider catalogue…' : 'Add / remove models…'}
          </button>
          {derr && <span className="error small">{derr}</span>}
        </div>
      ) : (
        <>
          <div className="row" style={{ margin: '6px 0' }}>
            <input value={q} placeholder={`filter ${available.length} models…`} autoFocus
              onChange={(e) => setQ(e.target.value)} style={{ width: 300 }} />
            <span className="muted small">{value.length} enabled</span>
            <button className="subtle" onClick={() => setAvailable(null)}>done</button>
          </div>
          <div className="model-checklist">
            {listed.slice(0, 200).map((id) => (
              <label key={id} className={`model-check ${value.includes(id) ? 'on' : ''}`}>
                <input type="checkbox" checked={value.includes(id)} onChange={() => toggle(id)} />
                <code>{id}</code>
              </label>
            ))}
            {listed.length > 200 && <div className="muted small" style={{ padding: 6 }}>…{listed.length - 200} more · narrow the filter</div>}
          </div>
        </>
      )}
      {help && <div className="muted small" style={{ marginTop: 4 }}>{help}</div>}
    </span>
  )
}

// A number field holds a number, or nothing when it is empty: AppTown
// refuses a quoted number ("2") or an empty string where a number belongs.
// A value saved as text before the field was a number is read back as one.
export const asNumber = (v: unknown): number | undefined => {
  if (typeof v === 'number') return Number.isFinite(v) ? v : undefined
  if (typeof v !== 'string' || v.trim() === '') return undefined
  const n = Number(v)
  return Number.isFinite(n) ? n : undefined
}

export function ConnectorForm({ spec, icon, initialName, initialConfig, onDone }: {
  spec: ConnectorEntry; icon: string; initialName: string; initialConfig: Record<string, unknown>
  // onDone gets the saved binding's name, or nothing when cancelled.
  onDone: (saved?: string) => void
}) {
  const [name, setName] = useState(initialName)
  const [cfg, setCfg] = useState(initialConfig)
  const [test, setTest] = useState<IntegrationHealth | null>(null)
  const tester = useAction()
  const saver = useAction(() => onDone(name))
  const testing = tester.busy
  const err = firstErr(tester.err, saver.err)

  const runTest = () => {
    setTest(null)
    void tester.run(async () => {
      setTest(await post<IntegrationHealth>('/integrations/test', { capability: spec.capability, provider: spec.provider, config: cfg }))
    })
  }

  const save = () => void saver.run(() => post('/integrations', { name, capability: spec.capability, provider: spec.provider, config: cfg }))

  return (
    <div style={{ maxWidth: 700 }}>
      <div className="row">
        <ConnIcon name={icon} size={30} />
        <div>
          <h1 style={{ marginBottom: 2 }}>{spec.title}</h1>
          <div className="muted small">{spec.description}</div>
        </div>
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <div className="form-grid" style={{ maxWidth: 660 }}>
          <label>Name</label>
          <span>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="my-scanner" autoFocus style={{ width: '100%' }} />
            <div className="muted small">Workflows refer to this integration by name; runs record which version they used.</div>
          </span>
          {(spec.fields ?? []).map((f) => (
            <span key={f.key} style={{ display: 'contents' }}>
              <label>{f.label}</label>
              {f.type === 'modelList' ? (
              <ModelListPicker
                value={Array.isArray(cfg[f.key]) ? (cfg[f.key] as string[]) : []}
                onChange={(v) => setCfg({ ...cfg, [f.key]: v })}
                providerName={name || spec.title} config={cfg} help={f.help} />
            ) : f.type === 'secretRef' ? (
                <SecretRefPicker
                  value={Array.isArray(cfg[f.key]) ? (cfg[f.key] as string[]) : String(cfg[f.key] ?? '').split(' ').filter(Boolean)}
                  onChange={(v) => setCfg({ ...cfg, [f.key]: v })}
                  help={f.help} />
              ) : f.type === 'bool' ? (
                <span><label className="small"><input type="checkbox" checked={!!cfg[f.key]}
                  onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.checked })} /> {f.help}</label></span>
              ) : f.type === 'number' ? (
                <span>
                  <input type="number" min={1} step={1} value={typeof cfg[f.key] === 'number' ? String(cfg[f.key]) : ''} placeholder={f.placeholder}
                    onChange={(e) => setCfg({ ...cfg, [f.key]: asNumber(e.target.value) })} style={{ width: 120 }} />
                  {f.help && <div className="muted small">{f.help}</div>}
                </span>
              ) : f.type === 'select' ? (
                <span>
                  <select value={String(cfg[f.key] ?? '')} onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}>
                    {(f.options ?? '').split(',').map((o) => <option key={o} value={o}>{o || '(none)'}</option>)}
                  </select>
                  {f.help && <div className="muted small">{f.help}</div>}
                </span>
              ) : (
                <span>
                  <input value={String(cfg[f.key] ?? '')} placeholder={f.placeholder}
                    onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}
                    style={{ width: '100%', fontFamily: f.type === 'command' ? 'var(--font-mono)' : undefined }} />
                  {f.help && <div className="muted small">{f.help}</div>}
                </span>
              )}
            </span>
          ))}
        </div>
      </div>

      {test && (test.checked ? (
        <div className="card row" style={{ borderColor: test.ok ? 'var(--ok)' : 'var(--bad)' }}>
          <span className={`dot ${test.ok ? 'ok' : 'bad'}`} aria-hidden />
          <span className="small"><strong>{test.ok ? 'Test passed' : 'Test failed'}</strong> — {test.detail}</span>
        </div>
      ) : (
        <div className="card row">
          <span className="small"><strong>No test for this kind</strong></span>
        </div>
      ))}
      {err && <div className="error">{err}</div>}
      <div className="row" style={{ marginTop: 12 }}>
        <button onClick={runTest} disabled={testing}>{testing ? 'Testing…' : 'Test connection'}</button>
        <span style={{ flex: 1 }} />
        <button onClick={() => onDone()}>Cancel</button>
        <button className="primary" onClick={save} disabled={saver.busy || !name.trim()}>Save</button>
      </div>
    </div>
  )
}
