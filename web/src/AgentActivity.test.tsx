// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import AgentActivity from './AgentActivity'

// A new or idle chat is the person's turn: a neutral word, not the amber
// "waiting for you" a change uses when a decision is due.
it('shows an idle chat as your turn, without a warning light', () => {
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<AgentActivity busy={false} connected />)
  const chip = el.querySelector('.chip')!
  expect(chip.textContent).toContain('your turn')
  expect(chip.textContent).not.toContain('waiting for you')
  expect(chip.classList.contains('pending')).toBe(false)
})
