// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { pageEntry, updated } from './pageUpdate'

it('does not report a deployment update for a Vite page with its client script first', () => {
  const doc = document.implementation.createHTMLDocument()
  doc.head.innerHTML = '<script type="module" src="/@vite/client"></script><script type="module" src="/src/main.tsx"></script>'
  expect(pageEntry(doc)).toBe('')
  expect(updated(pageEntry(doc), '/assets/index-shipped.js')).toBe(false)
})

it('compares the actual entry script for a production page', () => {
  const doc = document.implementation.createHTMLDocument()
  doc.head.innerHTML = '<script type="module" src="/assets/index-original.js"></script>'
  expect(pageEntry(doc)).toBe('/assets/index-original.js')
  expect(updated(pageEntry(doc), '/assets/index-new.js')).toBe(true)
})
