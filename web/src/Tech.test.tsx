import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Tech, proseBreaks } from './Tech'

it('breaks a technical value only after its separators', () => {
  expect(renderToStaticMarkup(<Tech>club-newsletter.apps.localhost:36257</Tech>))
    .toBe('club-<wbr/>newsletter.<wbr/>apps.<wbr/>localhost:<wbr/>36257')
  expect(renderToStaticMarkup(<Tech>apptown:change:create, apptown:user:read</Tech>))
    .toBe('apptown:<wbr/>change:<wbr/>create, apptown:<wbr/>user:<wbr/>read')
  expect(renderToStaticMarkup(<Tech>plain</Tech>)).toBe('plain')
})

it('breaks an address inside a sentence at its separators and leaves the words alone', () => {
  expect(renderToStaticMarkup(<>{proseBreaks('front door http://lunch-orders.apps.hilltopcycling.club/ → 200')}</>))
    .toBe('front door http://lunch-<wbr/>orders.<wbr/>apps.<wbr/>hilltopcycling.<wbr/>club/ → 200')
  expect(renderToStaticMarkup(<>{proseBreaks('the app was not running')}</>)).toBe('the app was not running')
})
