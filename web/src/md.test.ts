// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderMd } from './md'

// Every markdown sink in the UI goes through renderMd; these pin the two
// promises it makes — no scripting, and no restyling the page around it.
describe('renderMd', () => {
  it('renders markdown to html', () => {
    expect(renderMd('# hi\n\nsome *words*')).toContain('<h1>hi</h1>')
    expect(renderMd('# hi\n\nsome *words*')).toContain('<em>words</em>')
  })

  it('treats nothing as nothing', () => {
    expect(renderMd(null)).toBe('')
    expect(renderMd(undefined)).toBe('')
    expect(renderMd('')).toBe('')
  })

  it('strips <style> blocks so an embedded page cannot restyle AppTown', () => {
    const out = renderMd('<style>body { display: none }</style>\n\nhello')
    expect(out).not.toContain('<style')
    expect(out).not.toContain('display: none')
    expect(out).toContain('hello')
  })

  it('strips <script> and event handlers', () => {
    const out = renderMd('<script>alert(1)</script>\n\n<img src="x" onerror="alert(1)">\n\ntext')
    expect(out).not.toContain('<script')
    expect(out).not.toContain('alert(1)')
    expect(out).not.toContain('onerror')
    expect(out).toContain('text')
  })

  it('strips inline style= attributes', () => {
    const out = renderMd('<div style="position:fixed;inset:0">boxed</div>')
    expect(out).not.toContain('style=')
    expect(out).toContain('boxed')
  })

  it('strips <link>, <iframe> and <form>', () => {
    const out = renderMd('<link rel="stylesheet" href="x.css">\n\n<iframe src="x"></iframe>\n\n<form action="x"><input></form>')
    expect(out).not.toContain('<link')
    expect(out).not.toContain('<iframe')
    expect(out).not.toContain('<form')
  })
})
