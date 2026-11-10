import { siDigitalocean, siDocker, siFlydotio, siGooglecloud, siKubernetes, siPortainer } from 'simple-icons'

// The canonical mark for each kind of target, inlined from simple-icons
// (CC0): Docker's whale for a Docker host, Portainer, DigitalOcean, Fly.io,
// the Kubernetes wheel, Google Cloud. A kind without a mark gets a plain cloud.
// A mark whose brand colour does not read on a dark page is filled from a
// theme token instead (--mark-fly: Fly.io's purple, tinted lighter in dark).
const BRAND: Record<string, { path: string; hex: string; title: string; fill?: string }> = {
  'docker-host': siDocker, portainer: siPortainer, 'do-app': siDigitalocean, 'fly-machines': { ...siFlydotio, fill: 'var(--mark-fly)' }, kubernetes: siKubernetes, 'cloud-run': siGooglecloud,
}
export function Glyph({ kind }: { kind: string }) {
  const b = BRAND[kind]
  if (!b) return <svg viewBox="0 0 24 24" style={{ fill: 'none', stroke: 'currentColor', strokeWidth: 1.7 }}><path d="M7 18h11a4 4 0 0 0 .5-8A6 6 0 0 0 7 9a4.5 4.5 0 0 0 0 9z" /></svg>
  return <svg viewBox="0 0 24 24" role="img" aria-label={b.title} style={{ fill: b.fill ?? '#' + b.hex }}><path d={b.path} /></svg>
}
