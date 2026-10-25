import { useState } from 'react'
import { get, post, put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { AddressObservation, AddressSnapshot, DeploymentAddress } from '../../../wire.gen'

export function DeploymentAddressEdit({ slug, id }: { slug: string; id: string }) {
  const path = `/projects/${slug}/deployments/${id}/address`
  return <AddressForm key={path} path={path} />
}

function AddressForm({ path }: { path: string }) {
  const current = useGet<DeploymentAddress>(path)
  const [draft, setDraft] = useState<{ revision: number; hostname: string } | null>(null)
  const [observed, setObserved] = useState<AddressObservation | null>(null)
  const { run, busy, err } = useAction()
  if (!current.data) return <ReadStatus q={current} what="the deployment addresses" />
  const settings = current.data
  const legacy = settings.revision > 0 && !settings.verified
  return <section className="small" style={{ marginTop: 12 }}>
    <h4 style={{ marginBottom: 6 }}>Addresses</h4>
    <StaleNote q={current} />
    <p className="muted">Address changes take effect on the next release. Previous hostnames stay reserved until this deployment is taken down.</p>
    {settings.servingURL && <p>Last observed serving address: <code>{settings.servingURL}</code></p>}
    {settings.generatedHostname && <p>Generated address: <code>{settings.generatedHostname}</code></p>}
    {legacy ? <>
      <p>Confirm the addresses currently served by the provider before editing or releasing this existing deployment.</p>
      <button disabled={busy} onClick={async () => { const value = await run(() => get<AddressObservation>(`${path}/observation`)); if (value) setObserved(value) }}>Inspect current addresses</button>
      {observed && <div>
        <p>Observed hostnames: {observed.ingress.hosts.map((host) => <code key={host} style={{ marginRight: 8 }}>{host}</code>)}</p>
        <p>Certificate domain: <code>{observed.ingress.certificateDomain}</code></p>
        <button disabled={busy} onClick={async () => { const saved = await run(() => post<AddressObservation>(`${path}/observation`, observed)); if (saved) setObserved(null) }}>Confirm these addresses</button>
        <button disabled={busy} onClick={() => setObserved(null)}>Cancel</button>
      </div>}
    </> : <form onSubmit={async (event) => {
      event.preventDefault()
      const saved = await run(() => put<AddressSnapshot>(path, { expectedRevision: draft?.revision ?? settings.revision, customHostname: draft?.hostname ?? settings.customHostname }))
      if (saved) setDraft(null)
    }}>
      <label>Custom hostname <input disabled={busy} value={draft?.hostname ?? settings.customHostname} placeholder="app.example.com" onChange={(event) => setDraft({ revision: draft?.revision ?? settings.revision, hostname: event.target.value })} /></label>
      <button type="submit" disabled={busy || (!draft && settings.revision > 0)}>Save addresses</button>
      {draft && <button type="button" disabled={busy} onClick={() => setDraft(null)}>Discard changes</button>}
      <p className="muted">Leave empty to use the generated address. Configure DNS for custom hostnames separately.</p>
    </form>}
    {settings.reservedHosts.length > 0 && <details><summary>Reserved hostnames</summary>{settings.reservedHosts.map((host) => <div key={host}><code>{host}</code></div>)}</details>}
    {err && <div className="error" role="alert">{err}</div>}
  </section>
}
