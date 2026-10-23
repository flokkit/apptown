import { describe, expect, it } from 'vitest'
import { docRoute } from './docRoutes'

// A shipped doc's link to another shipped doc's file opens that doc's route
// on the Docs page; anything else is left to the browser.
describe('docRoute', () => {
  it('maps a link to a shipped doc to its route', () => {
    expect(docRoute('guides/server-setup.md', 'split-install.md')).toBe('/docs/guides/split-install')
    expect(docRoute('guides/server-setup.md', './split-install.md')).toBe('/docs/guides/split-install')
    expect(docRoute('guides/monitoring.md', '../integrations/registry-registry.admin.md')).toBe('/docs/registry/registry')
    expect(docRoute('guides/monitoring.md', '../integrations/model-pi.developer.md')).toBe('/docs/model/pi')
    expect(docRoute('integrations/llm-openrouter.admin.md', '../guides/secrets.md')).toBe('/docs/guides/secrets')
    expect(docRoute('integrations/llm-openrouter.admin.md', 'errors-glitchtip.developer.md')).toBe('/docs/errors/glitchtip')
  })

  it('leaves every other link as written', () => {
    for (const href of ['https://example.com/a.md', '/docs/guides/quickstart', '#top', 'mailto:a@b.c',
      '../../../design/x.md', '../api/openapi.json', 'split-install.md#workers', 'guides/x.md', '../../guides/x.md', '']) {
      expect(docRoute('guides/server-setup.md', href)).toBeNull()
    }
  })
})
