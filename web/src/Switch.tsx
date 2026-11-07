import './styles/switch.css'

/** An immediate, reversible setting; the caller saves and reports the result. */
export function Switch({ label, checked, disabled, onChange }: { label: string; checked: boolean; disabled?: boolean; onChange: (checked: boolean) => void }) {
  return <button type="button" className="setting-switch" role="switch" aria-label={label} aria-checked={checked} disabled={disabled} onClick={() => onChange(!checked)}><span className="setting-switch-knob" aria-hidden="true">{checked ? '✓' : ''}</span></button>
}
