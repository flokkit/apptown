import { ListRow } from '../../../ListRow'
import { Switch } from '../../../Switch'
import './Secrets.css'
import { Link } from 'react-router-dom'
import { Status } from '../../../Status'
import { NewChangeButton } from '../NewChange'
import { PRODUCT } from '../../../brand'
import { useState } from 'react'
import { del, get, post, put } from '../../../api'
import type { ProjectSecrets, SecretDeployment, SecretRevealed, SecretStatus, SecretStoreInfo as SecretStore } from '../../../wire.gen'
import { Modal } from '../../../Modal'
import { QuietConfirm } from '../../../QuietConfirm'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { relTime } from '../../../format'
import { useHolds } from '../../../instancePages'
import { Section } from '../../../Section'
import './people.css'

// Settings › Secrets: the app says which secrets it
// needs (secretspec.toml in the repo); this page says, per deployment,
// where each one comes from. Values go in and never come back out.

export function SecretsTab({ slug }: { slug: string }) {
  const [adding, setAdding] = useState(false)
  const q = useGet<ProjectSecrets>(`/projects/${slug}/secrets`)
  const { data, reload } = q
  const { run, err } = useAction(reload)
  const [editing, setEditing] = useState<{ env: string; name: string } | null>(null)
  const canReveal = useHolds('secret:reveal')
  const canBind = useHolds('secret:bind', slug)
  const [revealError, setRevealError] = useState('')
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const reveal = async (env: string, name: string) => {
    setRevealError('')
    try {
      const r = await get<SecretRevealed>(`/projects/${slug}/secrets/${env}/${name}/reveal`)
      setRevealed((m) => ({ ...m, [`${env}/${name}`]: r.value }))
    } catch (e) { setRevealError((e as Error).message) }
  }

  if (!data) return <ReadStatus q={q} what="the project's secrets" />

  if (data.problem) {
    return (
      <Section title="" label="Secrets">
        <StaleNote q={q} />
        <p className="error">{data.problem}</p>
      </Section>
    )
  }
  if (!data.declared) {
    return (
      <>
        <Section title="" label="Secrets" action={<button className="link" onClick={() => setAdding(true)}>Add secret</button>}>
          <StaleNote q={q} />
          <p className="muted">This app declares no secrets. Secrets appear here once its code declares them.</p>
        </Section>
        {adding && <NewChangeButton slug={slug} request={{ title: 'Add a secret', body: 'Declare the secret this app needs and explain where the app uses it. Ask me for its name and purpose before editing. Keep its value out of the code and this change; I will set the value in Settings after the change lands.' }} external={{ open: true, onClose: () => setAdding(false) }} />}
      </>
    )
  }

  return (
    <div className="secrets-page">
      {(data.ignored?.length ?? 0) > 0 && <p className="muted small">{PRODUCT} does not use these parts of the declaration: {data.ignored!.join('; ')}.</p>}
      <StaleNote q={q} />
      {err && <div className="error">{err}</div>}
      {revealError && <div role="alert" className="error">{revealError}</div>}

      {data.deployments.map((env) => (
        <DeploymentSection key={env.name} env={env} slug={slug} declared={data}
          onEdit={(name) => setEditing({ env: env.name, name })}
          onUnbind={(name) => run(() => del(`/projects/${slug}/secrets/${env.name}/${name}`))}
          canReveal={canReveal} canBind={canBind} revealed={revealed}
          onAgentVisible={(name, agentVisible) => run(() => put(`/projects/${slug}/secrets/${env.name}/${name}/agent-visibility`, { agentVisible }))}
          onSeparate={(name) => run(() => post(`/projects/${slug}/secrets/${env.name}/${name}/separate-preview`, {}))}
          onReveal={(name) => reveal(env.name, name)}
          onHide={(name) => setRevealed((m) => { const c = { ...m }; delete c[`${env.name}/${name}`]; return c })} />
      ))}

      {editing && (
        <BindModal slug={slug} env={editing.env} name={editing.name} stores={data.stores} agentVisible={editing.env === 'preview' && !!data.deployments.find((e) => e.name === editing.env)?.secrets.find((s) => s.name === editing.name)?.agentVisible}
          onClose={() => setEditing(null)} onDone={() => { setEditing(null); reload() }} />
      )}
    </div>
  )
}

// One section per deployment; each secret one row: its name, then where it
// goes and where its value comes from on a muted line, its acts on the right.
function DeploymentSection({ env, slug, declared, onEdit, onUnbind, canReveal, canBind, onAgentVisible, onSeparate, revealed, onReveal, onHide }: {
  env: SecretDeployment; slug: string; declared: ProjectSecrets
  onEdit: (name: string) => void; onUnbind: (name: string) => void
  canBind: boolean; onAgentVisible: (name: string, visible: boolean) => void; onSeparate: (name: string) => void
  canReveal: boolean; revealed: Record<string, string>; onReveal: (name: string) => void; onHide: (name: string) => void
}) {
  const consumersOf = (name: string) => {
    const d = declared.secrets?.find((s) => s.name === name)
    if (!d) return ''
    return d.allParts ? 'every part' : d.consumers.join(', ')
  }
  const missing = env.missing.length > 0
  return (
    <Section label={`Secrets for ${env.name}`}
      title={<>{env.name}{missing && <Status word="failed" help={`${env.missing.length} required secrets are missing. Set them below before deploying.`} />}</>}
      action={<Link to={`/projects/${slug}/deployments/${env.name}`}>Profile: {env.chosen || env.profile} · Deployment →</Link>}
      note={missing ? <>Nothing provides {env.missing.join(', ')} here. The {env.name === 'preview' ? 'preview' : 'deploy'} stops until it is set.</> : undefined}>
      {env.secrets.map((s) => {
        const shown = revealed[`${env.name}/${s.name}`]
        const goes = consumersOf(s.name)
        return (
          <ListRow key={s.name} className="card row secret-row secrets-card-secret-row" identity={
            <div className="secret-what">
              <code>{s.name}</code>{!s.required && <span className="muted small"> optional</span>}
              <div className="muted small">{goes}{goes && ' · '}<Source s={s} /></div>
              {env.name === 'preview' && s.bound && (
                <div className="secret-agent-access small">
                  {s.agentEligible ? <>
                    <label><Switch label={`${s.name}: agents working on changes can see this`} checked={s.agentVisible} disabled={!canBind} onChange={checked => onAgentVisible(s.name, checked)} /> Agents working on changes can see this</label>
                    {s.agentVisible && <><div>agents can see this</div><p className="muted">{agentWarning}</p></>}
                  </> : <>
                    <p className="muted">This value is shared with main. Give previews their own value to let agents see it</p>
                    <button className="link" disabled={!canBind} onClick={() => s.generated ? onSeparate(s.name) : onEdit(s.name)}>Give previews their own value</button>
                  </>}
                </div>
              )}
              {shown !== undefined && <div className="small"><code>{shown}</code> <button className="link" onClick={() => onHide(s.name)}>Hide</button></div>}
            </div>
            } action={<span className="row-action secret-acts">
              {canReveal && s.bound && s.store === 'internal' && shown === undefined && (
                <QuietConfirm label="Reveal…" className="subtle" ask={`Show the value of ${s.name} for ${env.name}? This read is recorded in the audit log under your name.`} onYes={() => onReveal(s.name)} />
              )}
              {s.bound && (
                <QuietConfirm label="Unbind" className="subtle" ask={`Forget ${s.name} for ${env.name}? ${s.store === 'internal' ? 'The stored value is deleted.' : 'The Infisical key stays where it is.'}`}
                  onYes={() => onUnbind(s.name)} />
              )}
              <button onClick={() => onEdit(s.name)}>{s.bound && !s.generated ? 'Change…' : 'Set…'}</button>
            </span>} />
        )
      })}
    </Section>
  )
}

function Source({ s }: { s: SecretStatus }) {
  if (s.bound && s.generated) {
    return <span>made by {PRODUCT} · built-in store v{s.version}</span>
  }
  if (s.bound && s.store === 'internal') {
    return <span>built-in store v{s.version} · set by {s.updatedBy}{s.updatedAt ? `, ${relTime(s.updatedAt)}` : ''}</span>
  }
  if (s.bound && s.store === 'infisical') {
    return <span>Infisical key <code>{s.ref}</code> · bound by {s.updatedBy}</span>
  }
  if (s.generated) return <span>{PRODUCT} will make one on the first boot</span>
  if (s.default) return <span>the declaration's default</span>
  if (!s.required) return <span>not set (optional)</span>
  return <Status word="failed" help="A required secret is missing. Set or bind this secret before deploying." />
}

const agentWarning = "Agents working on changes can see this value. Don’t reuse a password from a real deployment."

function BindModal({ slug, env, name, stores, agentVisible, onClose, onDone }: {
  slug: string; env: string; name: string; stores: SecretStore[]; agentVisible: boolean; onClose: () => void; onDone: () => void
}) {
  const infisical = stores.find((s) => s.name === 'infisical')
  const [store, setStore] = useState<'internal' | 'infisical'>('internal')
  const [value, setValue] = useState('')
  const [ref, setRef] = useState(name)
  const { run, busy, err } = useAction()
  const save = async () => {
    const body = store === 'internal' ? { store, value } : { store, ref }
    const r = await run(() => put(`/projects/${slug}/secrets/${env}/${name}`, body))
    if (r) onDone()
  }
  return (
    <Modal onClose={onClose} title={<h2 className="secrets-bind-modal-h2">{name} <span className="muted small">in {env}</span></h2>}>
      <div className="row secrets-bind-modal-div">
        <button className={store === 'internal' ? 'primary' : 'subtle'} onClick={() => setStore('internal')}>Type a value</button>
        <button className={store === 'infisical' ? 'primary' : 'subtle'} disabled={!infisical?.available}
          title={infisical?.available ? '' : infisical?.note} onClick={() => setStore('infisical')}>Name an Infisical key</button>
      </div>
      {store === 'internal' ? (
        <>
          <textarea aria-label="Secret value" rows={3} value={value} onChange={(e) => setValue(e.target.value)} placeholder="the value · pasted once, never shown again" autoFocus className="secrets-bind-modal-textarea" />
          <p className="muted small">Database backups include the encryption key and can reveal all secret values. Use Infisical for team secrets.</p>
        </>
      ) : (
        <>
          <input aria-label="Infisical key name" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="KEY_NAME in Infisical" autoFocus className="secrets-bind-modal-textarea" />
          <p className="muted small">The key must exist in the configured Infisical project and environment.</p>
        </>
      )}
      {agentVisible && <p className="muted small">{agentWarning}</p>}
      {err && <div className="error">{err}</div>}
      <div className="row secrets-bind-modal-div-2">
        <button className="primary" onClick={save} disabled={busy || (store === 'internal' ? !value.trim() : !ref.trim())}>Save</button>
        <button onClick={onClose}>Cancel</button>
      </div>
    </Modal>
  )
}
