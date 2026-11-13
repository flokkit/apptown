import { Link } from 'react-router-dom'
import { useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { Section } from '../../../Section'
import type { ProjectManifestView } from '../../../wire.gen'

// These facts describe the project's current wiring, beyond its adoption.
export function SetupFacts({ slug }: { slug: string }) {
  const q = useGet<ProjectManifestView>(`/projects/${slug}/manifest`)
  const m = q.data
  return <Section title="Project setup">
    <ReadStatus q={q} what="project setup" /><StaleNote q={q} />
    {m && <dl className="facts">
      <dt>Repository</dt><dd>{m.repo.backed ? <>Branch <code>{m.repo.defaultBranch}</code>{m.repo.remote && <> · remote <code>{m.repo.remote}</code></>}</> : 'Not repo-backed'}</dd>
      <dt>Guidance</dt><dd>{m.guidance.length} blocks · compiled sha256:<code>{m.guidanceHash}</code></dd>
      <dt>Bindings</dt><dd>{m.bindings.length ? m.bindings.map(b => <div key={`${b.capability}/${b.binding}`}>{b.capability} → {b.binding} v{b.version} · config sha256:<code>{b.configHash}</code></div>) : 'None'}<div><Link to="/integrations">Integrations</Link></div></dd>
    </dl>}
  </Section>
}
