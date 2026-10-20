// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderMd } from './md'
import { fillLiveLinks } from './liveLinks'
import type { DocLinks } from './wire.gen'

const known: DocLinks = {
  origin: { url: 'https://forge.example', path: '' },
  mcp: { url: 'https://forge.example/mcp/user', path: '/mcp/user' },
  git: { url: 'https://forge.example/git', path: '/git' },
}
const unknown: DocLinks = {
  origin: { url: '', path: '' },
  mcp: { url: '', path: '/mcp/user' },
  git: { url: '', path: '/git' },
}

function show(md: string, links: DocLinks): HTMLElement {
  const el = document.createElement('div')
  el.innerHTML = renderMd(md)
  fillLiveLinks(el, links)
  return el
}

describe('live links in a guide', () => {
  // Prose gets a link to AppTown's own address; code gets the address as
  // text to copy; a placeholder inside a link's text is not nested in another.
  it('name AppTown that knows its address', () => {
    const el = show('Connect at {apptown:mcp}.\n\n`APPTOWN_URL={apptown:origin}`\n\n```\ngit remote add apptown {apptown:git}/demo\n```\n\n[AppTown {apptown:origin}](/projects)', known)
    const links = [...el.querySelectorAll('a')].map((a) => [a.getAttribute('href'), a.textContent])
    expect(links).toEqual([['https://forge.example/mcp/user', 'https://forge.example/mcp/user'], ['/projects', 'AppTown https://forge.example']])
    expect(el.querySelector('p code')?.textContent).toBe('APPTOWN_URL=https://forge.example')
    expect(el.querySelector('pre code')?.textContent).toBe('git remote add apptown https://forge.example/git/demo\n')
    expect(el.textContent).not.toContain('{apptown:')
  })

  // Without a public address nothing links anywhere: prose says so in words
  // and code shows where the address goes.
  it('say so in words when AppTown does not know its address', () => {
    const el = show('Open {apptown:origin}. Connect at {apptown:mcp}.\n\n```\nclaude mcp add apptown {apptown:mcp}\n```', unknown)
    expect(el.querySelectorAll('a')).toHaveLength(0)
    expect(el.querySelector('p')?.textContent).toBe("Open AppTown's address. Connect at AppTown's address followed by /mcp/user.")
    expect(el.querySelector('pre code')?.textContent).toBe("claude mcp add apptown <AppTown's address>/mcp/user\n")
  })

  // An unknown name is left as written, for the docs test to catch.
  it('leave an unknown name as written', () => {
    const el = show('See {apptown:secret} and {apptown:mcp}', known)
    expect(el.textContent?.trim()).toBe('See {apptown:secret} and https://forge.example/mcp/user')
  })
})
