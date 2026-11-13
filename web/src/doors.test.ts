import { expect, it } from 'vitest'
import appRoutes from './App.tsx?raw'
import packs from './pages/Packs.tsx?raw'

it('gives the janitor its own route and keeps update approvals there', () => {
 expect(appRoutes).toContain('path="/janitor"')
 expect(import.meta.glob('./pages/Janitor.tsx')).not.toEqual({})
 expect(packs).toContain('to="/janitor"')
 expect(packs).not.toContain('KeepPacksCurrent')
})
