import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet, useRefresh } from '../../data'
import { ReadGate } from '../../ReadGate'
import { PageHead } from '../../PageHead'
import { Section } from '../../Section'
import { ListTable } from '../../ListTable'
import { Status } from '../../Status'
import { Modal } from '../../Modal'
import { fmtWhen } from '../../format'
import type { ConnectorCatalog, ConnectorEntry, ConnectorPreset, NotificationView, NotificationDelivery } from '../../wire.gen'
import { CatalogPicker } from './Catalog'
import { ConnectorForm } from './ConnectorForm'

function DeliveryState({ state, reason = '' }: { state: string; reason?: string }) {
  return <Status word={state === 'sent' ? 'passed' : state === 'failed' ? 'failed' : state === 'retrying' ? 'running' : 'unknown'} reason={state || 'No deliveries'} help={reason || (state ? `Latest delivery: ${state}.` : 'Send a test to check this channel.')} />
}
export function Notifications() {
  const q = useGet<NotificationView>('/notifications', { poll: 15_000 })
  const refresh = useRefresh('/notifications')
  const catalog = useGet<ConnectorCatalog>('/integrations/catalog')
  const [picking, setPicking] = useState(false)
  const [editing, setEditing] = useState<{ spec: ConnectorEntry; config: Record<string, unknown>; name: string } | null>(null)
  const test = useAction(undefined, { affects: ['/notifications', '/attention'] })
  const pick = (spec: ConnectorEntry, preset?: ConnectorPreset) => {
    setPicking(false)
    setEditing({ spec, config: preset?.config ?? {}, name: preset?.suggest ?? preset?.name ?? '' })
  }
  return <div>
    <PageHead page="notifications" />
    <p>Set a webhook address or SMTP password in <Link to="/integrations/secrets">Secrets</Link>, then add a channel.</p>
    <ReadGate q={q} what="notifications">{(data: NotificationView) => <>
      <Section title="Channels" action={<button onClick={() => setPicking(true)}>Add channel</button>}>
        {data.channels.length === 0 ? <p className="muted">No notification channels. Nothing is sent until you add one.</p> :
          <ListTable label="Notification channels" rows={data.channels} rowKey={c => c.name} rowTo={c => `/integrations/${c.name}`} columns={[
            { key: 'name', label: 'Channel', phone: 'name', render: c => <Link to={`/integrations/${c.name}`}>{c.label}</Link> },
            { key: 'state', label: 'Latest delivery', phone: 'state', render: c => <DeliveryState state={c.state} /> },
            { key: 'format', label: 'Format', render: c => c.kind === 'smtp' ? 'Email' : c.format },
            { key: 'hour', label: 'Digest (UTC)', render: c => `${String(c.digestHour).padStart(2, '0')}:00` },
            { key: 'test', label: '', phone: 'action', render: c => <button disabled={test.busy} onClick={() => void test.run(() => post<NotificationDelivery>(`/notifications/channels/${encodeURIComponent(c.name)}/test`, {}))}>Send test</button> },
          ]} />}
        {test.err && <p className="error">{test.err}</p>}
      </Section>
      <Section title="Recent deliveries">
        {data.deliveries.length === 0 ? <p className="muted">No deliveries yet.</p> :
          <ListTable label="Recent notification deliveries" rows={data.deliveries} rowKey={d => d.id ?? ""} columns={[
            { key: 'title', label: 'Notice', phone: 'name', render: d => d.title },
            { key: 'state', label: 'State', phone: 'state', render: d => <DeliveryState state={d.state} reason={d.reason} /> },
            { key: 'channel', label: 'Channel', render: d => <Link to={`/integrations/${d.channel}`}>{d.channel}</Link> },
            { key: 'kind', label: 'Kind', render: d => d.kind },
            { key: 'attempts', label: 'Attempts', kind: 'number', render: d => d.attempts },
            { key: 'time', label: 'Created', kind: 'time', render: d => fmtWhen(d.createdAt) },
          ]} />}
      </Section>
    </>}</ReadGate>
    {picking && <ReadGate q={catalog} what="channel catalog"><CatalogPicker catalog={(catalog.data?.catalog ?? []).filter(c => c.capability === 'notifier')} onPick={pick} onOpen={pick} onClose={() => setPicking(false)} /></ReadGate>}
    {editing && <Modal title="Add channel" onClose={() => setEditing(null)}><ConnectorForm spec={editing.spec} icon={editing.spec.icon ?? "notifications"} initialName={editing.name} initialConfig={editing.config} onDone={(saved) => { setEditing(null); if (saved) void refresh() }} /></Modal>}
  </div>
}
