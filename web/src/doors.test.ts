import { expect, it } from 'vitest'
import appRoutes from './App.tsx?raw'
import packs from './pages/Packs.tsx?raw'

it('keeps update approvals on Maintenance', () => {
 expect(appRoutes).toContain('path="/maintenance"')
 expect(import.meta.glob('./pages/Janitor.tsx')).not.toEqual({})
 expect(packs).toContain('to="/maintenance"')
 expect(packs).not.toContain('KeepPacksCurrent')
})
