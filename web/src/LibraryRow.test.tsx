import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { firstSentence, inlineCode } from './LibraryRow'

// A guidance summary keeps its inline code marked; the row sets it as code
// and nothing else in it is read as markdown.
it('sets backtick spans as code and leaves the rest as text', () => {
  expect(renderToStaticMarkup(<>{inlineCode('The final stage of a `build: .` part runs as a non-root `USER`.')}</>))
    .toBe('The final stage of a <code>build: .</code> part runs as a non-root <code>USER</code>.')
  expect(renderToStaticMarkup(<>{inlineCode('An unpaired ` stays, **as is**.')}</>)).toBe('An unpaired ` stays, **as is**.')
})

it('does not end a first sentence at a full stop inside code', () => {
  expect(firstSentence('Answer `GET /up. 200` quickly. Never touch the database.'))
    .toBe('Answer `GET /up. 200` quickly.')
  expect(firstSentence('No full stop here')).toBe('No full stop here')
})
