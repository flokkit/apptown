// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it } from 'vitest'
import { getKey, newQueryClient } from '../data'
import { UpdatesHistory } from './Janitor'

it('lists a released need refresh as a recent update linking to its release', () => {
 const client=newQueryClient()
 client.setQueryData(getKey('/janitor'),{projects:[]})
 client.setQueryData(getKey('/janitor/history?period=recent&offset=0'),{history:{changes:[{releaseId:'r',releaseNumber:9,slug:'lunch',project:'Lunch Orders',packVersions:[],packages:[{package:'postgres',from:'16.15',to:'16.16'}],landedAt:'2026-11-24T01:00:00Z'}],more:false}})
 const html=renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><UpdatesHistory/></QueryClientProvider></MemoryRouter>)
 expect(html).toContain('postgres 16.15 → 16.16')
 expect(html).toContain('href="/projects/lunch/releases/9"')
 expect(html).not.toContain('/changes/undefined')
 client.clear()
})
