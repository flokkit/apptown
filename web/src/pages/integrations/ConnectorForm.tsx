import { StatusDot } from '../../Status'
import './ConnectorForm.css'
import { HelpButton } from '../../PageHead'
import { useState } from 'react'
import { post } from '../../api'
import { ConnIcon } from '../../icons'
import { firstErr, useAction, useGet } from '../../data'
import type { ConnectorEntry, DiscoveredModels, IntegrationHealth, SecretNameList } from '../../wire.gen'
import { builtInModel } from './kinds'
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
        <div className="row connector-form-div" key={n}>
          <StatusDot word={unknown ? 'unknown' : available.includes(n) ? 'current' : 'missing'}
            help={unknown ? 'the secret store could not be read' : available.includes(n) ? `held in ${storeWords(namesQ.data?.stores?.[n])}` : 'not in the secret store yet · add this key to it'} />
          <code>{n}</code>
          {!unknown && !available.includes(n) && <span className="muted small">not resolved yet</span>}
          <button className="subtle danger" onClick={() => onChange(value.filter((v) => v !== n))}>remove</button>
        </div>
      ))}
      {unknown && <div className="error small connector-form-div">The secret store could not be read: {namesQ.err}</div>}
      <div className="row">
        {manual === null ? (
          <select aria-label="Secret key" value="" onChange={(e) => {
            if (e.target.value === '__other__') setManual('')
            else if (e.target.value) onChange([...value, e.target.value])
          }}>
            <option value="">{value.length === 0 ? 'Select a secret…' : 'Add another secret…'}</option>
            {unselected.map((n) => <option key={n} value={n}>{n}</option>)}
            <option value="__other__">Other (not in the secret store yet)…</option>
          </select>
        ) : (
          <>
            <input aria-label="Secret key name" value={manual} onChange={(e) => setManual(e.target.value)} placeholder="KEY_NAME" autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  const k = manual.trim()
                  if (k && !value.includes(k)) onChange([...value, k])
                  setManual(null)
                }
                if (e.key === 'Escape') setManual(null)
              }} className="connector-form-secret-ref-picker-input" />
            <button className="subtle" onClick={() => {
              const k = manual.trim()
              if (k && !value.includes(k)) onChange([...value, k])
              setManual(null)
            }}>add</button>
            <button className="subtle" onClick={() => setManual(null)}>cancel</button>
          </>
        )}
      </div>
      {help && <HelpButton about="Field" guide="server-setup">{help}</HelpButton>}
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
          <div className="row connector-form-model-list-picker-div">
            <input aria-label="Filter models" value={q} placeholder={`filter ${available.length} models…`} autoFocus
              onChange={(e) => setQ(e.target.value)} className="connector-form-model-list-picker-input" />
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
            {listed.length> 200 && <div className="muted small connector-form-model-list-picker-div-2">…{listed.length - 200} more · narrow the filter</div>}
          </div>
        </>
      )}
      {help && <HelpButton about="Field" guide="server-setup">{help}</HelpButton>}
    </span>
  )
}

function DefaultModelPicker({ value, enabled, name, config, onChange, catalog, onCatalog }: {
  value: string; enabled: string[]; name: string; config: Record<string, unknown>; onChange: (v: string) => void; catalog: string[]; onCatalog: (models: string[]) => void
}) {
  const discovery = useAction()
  const builtIn = builtInModel(name, config)
  const choices = (builtIn || enabled.length ? enabled : catalog).filter(id => !id.startsWith('~'))
  return <span>
    <select aria-label="Default model" value={choices.some(id => value === `${name}/${id}`) ? value : ''}
      onChange={e => onChange(e.target.value)}>
      <option value="">{value && !choices.some(id => value === `${name}/${id}`) ? `Choose a model (${value} is not selected)` : 'Choose a model…'}</option>
      {choices.map(id => <option key={id} value={`${name}/${id}`}>{id}</option>)}
    </select>
    {!builtIn && !enabled.length && <>
      <button className="subtle" disabled={discovery.busy} onClick={() => void discovery.run(async () => {
        onCatalog((await post<DiscoveredModels>('/integrations/discover', { name, config })).models)
      })}>Browse default models…</button>
      <label className="small">Fuzzy ~ selector <input aria-label="Fuzzy selector" value={value.startsWith(`${name}/~`) ? value.slice(name.length + 2) : ''}
        placeholder="model selector" onChange={e => onChange(e.target.value ? `${name}/~${e.target.value}` : '')} /></label>
    </>}
    {discovery.err && <span className="error small">{discovery.err}</span>}
  </span>
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
  const [defaultCatalog, setDefaultCatalog] = useState<string[]>([])
  const tester = useAction()
  const saver = useAction(() => onDone(name))
  const isModel = spec.capability === 'model'
  const custom = isModel && (icon === 'gpu' || !!initialConfig.baseUrl || !!initialConfig.providerDef || !!initialConfig.api || !!initialConfig.modelsJson)
  const enabled = Array.isArray(cfg.models) ? cfg.models as string[] : []
  const builtIn = isModel && builtInModel(name, cfg)
  const defaultChoices = (builtIn || enabled.length ? enabled : defaultCatalog).filter(id => !id.startsWith('~'))
  const fuzzyDefault = !builtIn && enabled.length === 0 && String(cfg.model).startsWith(`${name}/~`) && String(cfg.model).length > name.length + 2
  const invalidDefault = isModel && !!cfg.model && !defaultChoices.some(id => cfg.model === `${name}/${id}`) && !fuzzyDefault
  const fields = (spec.fields ?? []).filter(f => !isModel || (custom
    ? !['secretEnv', 'models'].includes(f.key)
    : !['baseUrl', 'api', 'apiKeyFrom', 'modelsJson'].includes(f.key)))
  const testing = tester.busy
  const err = firstErr(tester.err, saver.err)

  const runTest = () => {
    setTest(null)
    void tester.run(async () => {
      setTest(await post<IntegrationHealth>('/integrations/test', { capability: spec.capability, provider: spec.provider, config: cfg }))
    })
  }

  const save = () => !invalidDefault && void saver.run(() => post('/integrations', { name, capability: spec.capability, provider: spec.provider, config: cfg }))

  return (
    <div className="connector-form-div-2">
      <div className="row">
        <ConnIcon name={icon} size={30} />
        <div>
          <h1 className="connector-form-h1">{spec.title}</h1>
          <div className="muted small">{spec.description}</div>
        </div>
      </div>

      <div className="card mt-4" >
        <div className="form-grid connector-form-form-grid">
          <label>Name</label>
          <span>
            <input aria-label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-scanner" autoFocus className="connector-form-input" />
            <div className="muted small">Workflows refer to this integration by name; runs record which version they used.</div>
          </span>
          {fields.map((f) => (
            <span key={f.key} className="connector-form-span">
              <label>{f.label}</label>
              {isModel && f.key === 'model' ? (
                <span><DefaultModelPicker value={String(cfg.model ?? '')} enabled={enabled} name={name} config={cfg} catalog={defaultCatalog} onCatalog={setDefaultCatalog} onChange={v => setCfg({ ...cfg, model: v })} />
                {f.help && <HelpButton about="Default model" guide="server-setup">{f.help}</HelpButton>}</span>
              ) : f.type === 'modelList' ? (
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
                  <input aria-label={f.label} type="number" min={f.min ?? 1} max={f.max ?? undefined} step={1} value={typeof cfg[f.key] === 'number' ? String(cfg[f.key]) : ''} placeholder={f.placeholder}
                    onChange={(e) => setCfg({ ...cfg, [f.key]: asNumber(e.target.value) })} className="connector-form-input-2" />
                  {f.help && <HelpButton about={f.label || f.key} guide="server-setup">{f.help}</HelpButton>}
                </span>
              ) : f.type === 'select' ? (
                <span>
                  <select aria-label={f.label} value={String(cfg[f.key] ?? '')} onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}>
                    {(f.options ?? '').split(',').map((o) => <option key={o} value={o}>{o || '(none)'}</option>)}
                  </select>
                  {f.help && <HelpButton about={f.label || f.key} guide="server-setup">{f.help}</HelpButton>}
                </span>
              ) : (
                <span>
                  <input aria-label={f.label} value={String(cfg[f.key] ?? '')} placeholder={f.placeholder}
                    onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value })}
                    className={f.type === 'command' ? 'connector-form-input text-code' : 'connector-form-input'} />
                  {f.help && <HelpButton about={f.label || f.key} guide="server-setup">{f.help}</HelpButton>}
                </span>
              )}
            </span>
          ))}
        </div>
      </div>

      {test && (test.checked ? (
        <div className="card row" style={{ borderColor: test.ok ? 'var(--ok)' : 'var(--bad)' }}>
          <StatusDot word={test.ok ? 'passed' : 'failed'} />
          <span className="small"><strong>{test.ok ? 'Test passed' : 'Test failed'}</strong> — {test.detail}</span>
        </div>
      ) : (
        <div className="card row">
          <span className="small"><strong>No test for this kind</strong></span>
        </div>
      ))}
      {invalidDefault && <div className="error">{builtIn || enabled.length ? 'Choose a default model from Enabled models.' : 'Browse the live catalog and choose a default model.'}</div>}
      {err && <div className="error">{err}</div>}
      <div className="row mt-3" >
        {spec.capability !== 'notifier' && <button onClick={runTest} disabled={testing}>{testing ? 'Testing…' : 'Test connection'}</button>}
        <span className="grow" />
        <button onClick={() => onDone()}>Cancel</button>
        <button className="primary" onClick={save} disabled={saver.busy || !name.trim() || invalidDefault}>Save</button>
      </div>
    </div>
  )
}
