import './styles/spinner.css'

// The smaller muted rings fit beside conformance counts; change progress
// inherits its surrounding text colour.
export function Spinner({ size = 'normal' }: { size?: 'normal' | 'small' | 'tiny' }) {
  return <span className={`spinner spinner-${size}`} role="status" aria-label="running" />
}
