import { PRODUCT } from '../brand'
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { renderMd } from '../md'
import mermaid from 'mermaid'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { DocGuide, DocLinks, DocsIndex, IntegrationDocs } from '../wire.gen'
import { useDarkTheme } from '../theme'
import { fillLiveLinks } from '../liveLinks'

// Shipped documentation: guides plus the per-integration admin/developer
// documents. Everything renders from markdown embedded in the forged binary,
// so docs version with the release.

// MdDoc renders markdown, fills a guide's live links with this forge's own
// addresses (liveLinks.ts), and then draws any ```mermaid fences as
// diagrams, in the page's own theme (redrawn when a person switches it).
export function MdDoc({ content, links }: { content: string; links?: DocLinks }) {
  const ref = useRef<HTMLDivElement>(null)
  const dark = useDarkTheme()
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.innerHTML = renderMd(content)
    if (links) fillLiveLinks(el, links)
    el.querySelectorAll('code.language-mermaid').forEach((code) => {
      const holder = document.createElement('pre')
      holder.className = 'mermaid'
      holder.textContent = code.textContent ?? ''
      holder.style.background = 'transparent'
      ;(code.closest('pre') ?? code).replaceWith(holder)
    })
    const nodes = el.querySelectorAll<HTMLElement>('pre.mermaid')
    if (nodes.length) {
      mermaid.initialize({ startOnLoad: false, theme: dark ? 'dark' : 'neutral', securityLevel: 'strict' })
      mermaid.run({ nodes }).catch(() => { /* a broken diagram shows its own error box */ })
    }
  }, [content, links, dark])
  return <div className="md-doc" ref={ref} />
}

// The page opens on the first guide of the first group (Start here): the
// groups and their order come from the shipped guide index, not file names.
function firstPage(m: DocsIndex): string | null {
  const g = m.groups.find((group) => group.guides.length)?.guides[0]
  if (g) return `/docs/guides/${g.slug}`
  const i = m.integrations[0]
  return i ? `/docs/${i.capability}/${i.provider}` : null
}

export default function DocsPage() {
  const { section, slug } = useParams()
  const nav = useNavigate()
  const q = useGet<DocsIndex>('/docs', { kind: 'config' })
  const manifest = q.data
  const first = manifest ? firstPage(manifest) : null
  useEffect(() => {
    if (!section && first) nav(first, { replace: true })
  }, [first, section, nav])

  if (!manifest) return <ReadStatus q={q} what="the documentation list" />

  return (
    <div>
      <h1>Docs</h1>
      <StaleNote q={q} />
      {!first ? <div className="muted">{PRODUCT} ships no documentation.</div> : (
        <div className="docs-layout">
          {/* On a phone the list is a picker above the page, so the page
              keeps the screen's width (styles.css .docs-pick). */}
          <select className="docs-pick" aria-label="document" value={section && slug ? `/docs/${section}/${slug}` : ''}
            onChange={(e) => nav(e.target.value)}>
            {manifest.groups.map((group) => (
              <optgroup key={group.title} label={group.title}>
                {group.guides.map((g) => <option key={g.slug} value={`/docs/guides/${g.slug}`}>{g.title}</option>)}
              </optgroup>
            ))}
            {manifest.integrations.length > 0 && (
              <optgroup label="Integrations">
                {manifest.integrations.map((i) => <option key={`${i.capability}-${i.provider}`} value={`/docs/${i.capability}/${i.provider}`}>{i.title}</option>)}
              </optgroup>
            )}
          </select>
          <nav className="conn-subnav">
            {manifest.groups.map((group) => (
              <div key={group.title} className="docs-group" role="group" aria-label={group.title}>
                <h2 className="docs-group-title">{group.title}</h2>
                {group.guides.map((g) => (
                  <button
                    key={g.slug}
                    className={'conn-subnav-item' + (section === 'guides' && slug === g.slug ? ' active' : '')}
                    onClick={() => nav(`/docs/guides/${g.slug}`)}
                  >{g.title}</button>
                ))}
              </div>
            ))}
            {manifest.integrations.length > 0 && (
              <div className="docs-group" role="group" aria-label="Integrations">
                <h2 className="docs-group-title">Integrations</h2>
                {manifest.integrations.map((i) => (
                  <button
                    key={`${i.capability}-${i.provider}`}
                    className={'conn-subnav-item' + (section === i.capability && slug === i.provider ? ' active' : '')}
                    onClick={() => nav(`/docs/${i.capability}/${i.provider}`)}
                  >{i.title}</button>
                ))}
              </div>
            )}
          </nav>
          <div className="docs-body">
            {section === 'guides' && slug && <GuideDoc slug={slug} />}
            {section && section !== 'guides' && slug && <IntegrationDoc capability={section} provider={slug} />}
          </div>
        </div>
      )}
    </div>
  )
}

function GuideDoc({ slug }: { slug: string }) {
  const q = useGet<DocGuide>(`/docs/guides/${slug}`)
  const data = q.data
  if (!data) return <ReadStatus q={q} what="this guide" />
  return <>
    <StaleNote q={q} />
    <MdDoc content={data.content} links={data.links} />
  </>
}

function IntegrationDoc({ capability, provider }: { capability: string; provider: string }) {
  const q = useGet<IntegrationDocs>(`/integrations/docs/${capability}/${provider}`)
  const docs = q.data
  const [tab, setTab] = useState<'admin' | 'developer'>('developer')
  if (!docs) return <ReadStatus q={q} what="this integration's documentation" />
  return (
    <div>
      <StaleNote q={q} />
      <div className="tabs" style={{ marginBottom: 12 }}>
        <button className={'tab' + (tab === 'developer' ? ' active' : '')} onClick={() => setTab('developer')}>Developer</button>
        <button className={'tab' + (tab === 'admin' ? ' active' : '')} onClick={() => setTab('admin')}>Admin</button>
      </div>
      <MdDoc content={tab === 'admin' ? docs.admin : docs.developer} />
    </div>
  )
}
