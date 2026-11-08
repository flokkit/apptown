import './styles/progress-bar.css'

// A count of completed work. Without a total the bar stays indeterminate.
export function ProgressBar({ label, value, total }: { label: string; value: number; total?: number }) {
  return <progress className="progress-bar" aria-label={label} value={total ? value : undefined} max={total} />
}
