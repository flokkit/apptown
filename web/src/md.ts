import { marked } from 'marked'
import DOMPurify from 'dompurify'
import policy from '../resource-policy.json' with { type: 'json' }

// Render untrusted markdown to SANITIZED HTML for dangerouslySetInnerHTML.
//
// marked emits raw HTML by design — its `sanitize` option was removed in v0.7,
// so `<script>`, `onerror=`, and stray tags in the source pass straight
// through. Almost everything we render as markdown is untrusted: agent output,
// artifact bodies, so-far notes, ADR/guidance bodies, imported docs. Any of
// those reaching dangerouslySetInnerHTML raw is stored HTML injection / XSS.
// Every markdown sink in the UI MUST go through here.
//
// Two things must be neutralised, not just scripting:
//   1. Scripting/XSS — DOMPurify's default job.
//   2. Global RESTYLE — DOMPurify KEEPS <style>/<link> and inline `style` by
//      default. An agent report that embeds a self-contained HTML page (a
//      `<style>body{…}</style>` block) would otherwise leak its `body{}` rules
//      into AppTown's global stylesheet and hijack the whole page's layout — this
//      is exactly what mangled change 146. Forbid the tags and attribute that
//      let embedded HTML reach outside its own fragment.
const SANITIZE = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['style', 'link', 'base', 'meta', 'title', 'iframe', 'form', 'picture', 'source', 'video', 'audio'],
  FORBID_ATTR: ['style', 'srcset'],
}

export function renderMd(src: string | null | undefined): string {
  const document = window.document
  const html = marked.parse(src ?? '') as string
  // The fragment stays inert until every image source has been classified.
  const fragment = DOMPurify.sanitize(html, { ...SANITIZE, RETURN_DOM_FRAGMENT: true })
  // GFM task lists carry completion in disabled checkboxes. Retain that
  // meaning without admitting image inputs or interactive form controls.
  fragment.querySelectorAll('input').forEach((input) => {
    if (input.type !== 'checkbox') { input.remove(); return }
    const task = document.createElement('input')
    task.type = 'checkbox'
    task.disabled = true
    if (input.checked) task.setAttribute('checked', '')
    input.replaceWith(task)
  })
  fragment.querySelectorAll('img').forEach((img) => {
    const raw = img.getAttribute('src') ?? ''
    let url: URL | undefined
    try { url = new URL(raw, document.baseURI) } catch { /* invalid source becomes text */ }
    const http = raw && url && ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password
    const local = http && (url!.origin === window.location.origin || policy.imageOrigins.includes(url!.origin))
    const raster = /^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i.test(raw)
    if (raw && (local || raster)) {
      img.setAttribute('src', raster ? raw : url!.href)
      return
    }
    const fallback = document.createElement(http ? 'a' : 'span')
    const alt = img.getAttribute('alt')?.trim()
    fallback.textContent = alt ? `${alt} (external image)` : 'External image'
    if (http) fallback.setAttribute('href', url!.href)
    img.replaceWith(fallback)
  })
  const container = document.createElement('div')
  container.append(fragment)
  return container.innerHTML
}
