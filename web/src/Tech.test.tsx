import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Tech } from './Tech'

it('breaks a technical value only after its separators', () => {
  expect(renderToStaticMarkup(<Tech>club-newsletter.apps.localhost:36257</Tech>))
    .toBe('club-<wbr/>newsletter.<wbr/>apps.<wbr/>localhost:<wbr/>36257')
  expect(renderToStaticMarkup(<Tech>apptown:change:create, apptown:user:read</Tech>))
    .toBe('apptown:<wbr/>change:<wbr/>create, apptown:<wbr/>user:<wbr/>read')
  expect(renderToStaticMarkup(<Tech>plain</Tech>)).toBe('plain')
})
