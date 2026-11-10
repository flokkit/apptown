// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import TextRoles from './TextRoles'
import { TEXT_ROLES } from '../textRoles'
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readFileSync } from 'node:fs'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('text roles', () => {
  it('shows a sample of every role with its class', () => {
    const el = document.createElement('div')
    act(() => createRoot(el).render(<MemoryRouter><TextRoles /></MemoryRouter>))
    for (const role of TEXT_ROLES) expect(el.querySelector(`td .${role.className}`)?.textContent, role.role).toBe(role.sample)
  })

  it('lists exactly the role classes the stylesheets declare, and DESIGN.md names each', () => {
    const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
    const [typography, text, design] = ['../styles/typography.css', '../styles/text.css', '../../DESIGN.md'].map(read)
    const declared = new Set([...typography.matchAll(/\.(text-[a-z]+|group-label)\b/g)].map(m => m[1]))
    for (const name of ['small', 'muted']) if (new RegExp(`^\\.${name} \\{`, 'm').test(text)) declared.add(name)
    expect([...declared].sort()).toEqual(TEXT_ROLES.map(r => r.className).sort())
    for (const role of TEXT_ROLES) expect(design, role.role).toContain(`| ${role.role} |`)
  })
})
