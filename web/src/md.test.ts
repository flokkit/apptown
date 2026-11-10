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

describe('Markdown image resources', () => {
  function fragment(source: string) {
    const el = document.createElement('div')
    el.innerHTML = renderMd(source)
    return el
  }
  it('keeps local and exact tour images, resolving relative URLs', () => {
    for (const src of ['/picture.png', 'picture.png', `${location.origin}/picture.png`, `//${location.host}/picture.png`, 'https://apptown.dev/tour/v1/x.png']) {
      expect(fragment(`![picture](${src})`).querySelector('img')?.src).toBe(new URL(src, document.baseURI).href)
    }
  })
  it('turns remote HTTP images into labelled links without fetch surfaces', () => {
    for (const src of ['https://elsewhere.test/a.png', '//elsewhere.test/a.png', 'https://apptown.dev.evil.test/a.png', 'https://evil-apptown.dev/a.png', 'http://apptown.dev/a.png', 'https://apptown.dev:444/a.png']) {
      const el = fragment(`![Example](${src})`)
      expect(el.querySelector('img, source, picture, svg, video, audio')).toBeNull()
      expect(el.querySelector('a')?.textContent).toBe('Example (external image)')
      expect(el.querySelector('a')?.href).toBe(new URL(src, document.baseURI).href)
    }
    expect(fragment('<img src="https://remote.test/p.png">').textContent).toBe('External image')
    expect(fragment('<img src="https://remote.test/p.png" alt="&lt;script&gt;">').querySelector('script')).toBeNull()
  })
  it('admits raster data only and makes unsafe schemes and credentials nonclickable', () => {
    for (const mime of ['png', 'jpeg', 'webp', 'gif']) {
      expect(fragment(`<img src="data:image/${mime};base64,YQ==">`).querySelector('img')).not.toBeNull()
    }
    for (const src of ['javascript:alert(1)', 'data:text/html;base64,YQ==', 'data:image/svg+xml;base64,YQ==', 'blob:https://apptown.dev/id', 'https://user@apptown.dev/a.png', 'https://[bad/a']) {
      const el = fragment(`<img src="${src}">`)
      expect(el.querySelector('img,a')).toBeNull()
      expect(el.textContent).toBe('External image')
    }
  })
  it('preserves task completion while removing image inputs', () => {
    const el = fragment('- [x] Done\n- [ ] Pending\n\n<input type="image" src="https://elsewhere.test/input.png"><input type="checkbox" checked src="https://elsewhere.test/disguised.png">')
    const tasks = [...el.querySelectorAll('li input')]
    expect(tasks).toHaveLength(2)
    expect(tasks.map(input => (input as HTMLInputElement).checked)).toEqual([true, false])
    expect([...el.querySelectorAll('input')].every(input => input.type === 'checkbox' && input.disabled)).toBe(true)
    expect(el.querySelector('input[src], input[type="image"]')).toBeNull()
  })
  it('removes alternative fetch paths while retaining an allowed image', () => {
    const el = fragment('<picture><source srcset="https://evil.test/one"><img src="/good.png" srcset="https://evil.test/two 2x"></picture><svg><image href="https://evil.test/three" /></svg><video poster="https://evil.test/four"></video>')
    expect(el.querySelectorAll('img')).toHaveLength(1)
    expect(el.querySelector('img')?.hasAttribute('srcset')).toBe(false)
    expect(el.querySelector('source,picture,svg,video')).toBeNull()
  })
})

it('shows embedded disclosures as open sections when the change page requests it', () => {
  const text = '<details><summary>Recorded commands</summary><p>npm test</p><details><summary>Result</summary><p>Passed</p></details></details>'
  const el = document.createElement('div'); el.innerHTML = renderMd(text)
  expect(el.querySelector('details, summary')).toBeNull()
  expect(el.textContent).toBe('Recorded commandsnpm testResultPassed')
  expect(renderMd(text)).not.toContain('<details>')
  expect(renderMd(text)).toContain('<section><h4>Recorded commands</h4>')
})

it('keeps short code in a sentence whole, spaces or not, and lets long code wrap', () => {
  const html = renderMd('Leave `--worker-url` unset; `' + 'x'.repeat(40) + '` and `sh -s -- stop` are commands.\n\n```\n--in-a-block\n```')
  const box = document.createElement('div')
  box.innerHTML = html
  const [flag, long, spaced] = box.querySelectorAll('p code')
  expect(flag.classList.contains('nobreak')).toBe(true)
  expect(long.classList.contains('nobreak')).toBe(false)
  expect(spaced.classList.contains('nobreak')).toBe(true)
  expect(box.querySelector('pre code')!.classList.contains('nobreak')).toBe(false)
})

it('breaks long code in a sentence at its separators, not inside a word', () => {
  const box = document.createElement('div')
  box.innerHTML = renderMd('Pass `--providers.docker.exposedbydefault=false` to Traefik.\n\n```\n--providers.docker.exposedbydefault=false\n```')
  expect(box.querySelector('p code')!.innerHTML).toBe('--providers.<wbr>docker.<wbr>exposedbydefault=<wbr>false')
  expect(box.querySelector('pre code')!.querySelector('wbr')).toBeNull()
})

it('breaks long code with spaces only at its spaces, each short piece whole', () => {
  const box = document.createElement('div')
  box.innerHTML = renderMd('Run `curl -fsSL https://get.docker.com | sh` first, then `-console-url http://172.17.0.1:7778 -x ' + 'y'.repeat(20) + '` and `run --path=/srv/apps/club-newsletter/releases/current/public`.')
  const [curl, consoleUrl, path] = box.querySelectorAll('p code')
  expect(curl.querySelector('wbr')).toBeNull()
  expect([...curl.querySelectorAll('span.nobreak')].map(s => s.textContent)).toContain('https://get.docker.com')
  expect(curl.textContent).toBe('curl -fsSL https://get.docker.com | sh')
  expect(consoleUrl.querySelector('wbr')).toBeNull()
  // A piece longer than 32 characters still breaks at its separators.
  expect(path.querySelector('wbr')).not.toBeNull()
  expect(path.textContent).toBe('run --path=/srv/apps/club-newsletter/releases/current/public')
})
