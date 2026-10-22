import { marked } from 'marked'
import DOMPurify from 'dompurify'

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
  FORBID_TAGS: ['style', 'link', 'base', 'meta', 'title', 'iframe', 'form'],
  FORBID_ATTR: ['style'],
}

export function renderMd(src: string | null | undefined): string {
  const html = marked.parse(src ?? '') as string
  return DOMPurify.sanitize(html, SANITIZE)
}
