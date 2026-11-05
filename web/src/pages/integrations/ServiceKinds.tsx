import { Status } from '../../Status'
import { useState } from 'react'
import { del, post } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ServiceKindList } from '../../wire.gen'

// --- Runtime-defined service kinds ---
// A service kind declares a capability whose contributions are data: env
// templates, steering, guidance, resource fields. New org services without
// touching the binary; provisioning stays adapter territory.


const KIND_EXAMPLE = `capability: sso
title: SSO (OIDC)
description: Organisational single sign-on; products wire OIDC middleware.
connector:
  fields:
    - key: url
      label: Issuer base URL
      placeholder: https://auth.example.com
resource:
  fields:
    - key: client_id
      label: OIDC client ID
    - key: client_secret_from
      label: Client secret (secret name)
      type: secretRef
env:
  - key: OIDC_ISSUER
    value: "{{connector.url}}"
  - key: OIDC_CLIENT_ID
    value: "{{resource.client_id}}"
  - key: OIDC_CLIENT_SECRET
    value: "{{secret:resource.client_secret_from}}"
steering: |
  This product authenticates via organisational SSO (OIDC). Issuer, client
  id and secret are injected as OIDC_ISSUER / OIDC_CLIENT_ID /
  OIDC_CLIENT_SECRET — wire the middleware from env, never hardcode.
`

export function ServiceKindsPanel() {
  const kindsQ = useGet<ServiceKindList>('/services', { kind: 'config' })
  const kinds = kindsQ.data?.services ?? []
  const [editing, setEditing] = useState<string | null>(null)
  const { run, busy, err } = useAction()

  const save = () => run(async () => { await post('/services', { yaml: editing }); setEditing(null) })
  const remove = (capability: string) => run(() => del(`/services/${capability}`))

  return (
    <div>
      <div className="muted small mb-3" >
        Projects attach these under Project settings › Services.
      </div>
      <ReadStatus q={kindsQ} what="the shared services" />
      <StaleNote q={kindsQ} />
      {kinds.map((k) => (
        <div key={k.capability} className="card">
          <div className="row">
            <strong>{k.title}</strong>
            <span className="badge">{k.capability}</span>
            {k.error && <Status word="failed" help={`${k.error} Correct its definition.`} />}
            <span className="grow" />
            <button className="subtle" onClick={() => setEditing(k.yaml)}>Edit</button>
            <QuietConfirm label="Delete" ask={`Delete the shared service "${k.capability}"?`} onYes={() => void remove(k.capability)} />
          </div>
          {k.description && <div className="muted small mt-1" >{k.description}</div>}
          <div className="muted small mt-1" >
            {(k.env ?? []).map((e) => e.key).join(', ') || 'no environment variables'}
          </div>
        </div>
      ))}
      {editing === null ? (
        <button onClick={() => setEditing(KIND_EXAMPLE)}>+ Define a shared service</button>
      ) : (
        <div className="card">
          <textarea aria-label="Service kind definition" value={editing} onChange={(e) => setEditing(e.target.value)} rows={20}
            style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 'var(--fs-xs)' }} />
          <div className="row mt-2" >
            <span className="grow" />
            <button onClick={() => setEditing(null)}>Cancel</button>
            <button className="primary" disabled={busy} onClick={() => void save()}>Save</button>
          </div>
        </div>
      )}
      {err && <div className="error">{err}</div>}
    </div>
  )
}
