import { FieldRow } from '../../../FieldRow'
import type { RoutineUpdates as Settings } from '../../../wire.gen'

// Fields in Releases; its form owns the draft and the revision-guarded save.
export function RoutineUpdates({ value, disabled, moved, onChange }: {
  value: Settings; disabled: boolean; moved: boolean; onChange: (value: Settings) => void
}) {
  return <>
    <FieldRow label="Routine updates"><select aria-label="Routine updates" disabled={disabled || moved} value={value?.mode || 'automatic'} onChange={event => onChange(event.target.value === 'window' ? { mode: 'window', days: 'sun', from: '02:00', to: '04:00', zone: Intl.DateTimeFormat().resolvedOptions().timeZone } : { mode: event.target.value })}>
      <option value="automatic">Automatically</option><option value="window">In a release window</option><option value="person">When a person says</option>
    </select></FieldRow>
    {value?.mode === 'window' && <>
      <FieldRow label="Days"><input aria-label="Window days" value={value.days || ''} placeholder="sun,mon" disabled={disabled} onChange={e => onChange({ ...value, days: e.target.value })} /></FieldRow>
      <FieldRow label="From"><input type="time" aria-label="Window from" value={value.from || ''} disabled={disabled} onChange={e => onChange({ ...value, from: e.target.value })} /></FieldRow>
      <FieldRow label="To"><input type="time" aria-label="Window to" value={value.to || ''} disabled={disabled} onChange={e => onChange({ ...value, to: e.target.value })} /></FieldRow>
      <FieldRow label="Time zone"><input aria-label="Window time zone" value={value.zone || ''} disabled={disabled} onChange={e => onChange({ ...value, zone: e.target.value })} /></FieldRow>
    </>}
  </>
}
