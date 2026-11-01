import React from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../src/data'
import TopBar from '../src/TopBar'
import { fmtWhen } from '../src/format'
import Attention from '../src/pages/Attention'
import { Capacity } from '../src/pages/integrations/Capacity'
import { ResourceTables } from '../src/pages/Infrastructure'
import Integrations from '../src/pages/Integrations'
import { FindingsTab } from '../src/pages/project/Security'
import '../src/fonts'
import '../src/styles.css'
const params = new URLSearchParams(location.search)
document.documentElement.dataset.theme = params.get('theme') || 'light'
const at = new Date(Date.now() - 5 * 60000).toISOString()
const oldAt = new Date(Date.now() - 25 * 60000).toISOString()
const fig = (value: number) => ({known: true, value, readAt: at})
const sums = {apptown: fig(4.23 * 2 ** 30), other: fig(0)}
const counts = {apptown: 9, other: 0}
const capacity = {kind:'docker-host', elastic:false, readAt:at, tenantsAt:at, tenants:Array.from({length:9},(_,i)=>({name:`Calendar ${i+1}`,kind:'deployment',project:`calendar-${i+1}`,deployment:'main',containers:i===0?4:3,used:fig(20*2**20),promised:fig(4.23*2**30/9),unlimited:i===0?3:2,cpus:0,cpuUnread:0,cpuUnlimited:i===0?4:3,unknown:false})), memory:{total:fig(1.93*2**30), promised:{sum:sums, limited:counts, unlimited:{apptown:19,other:0}, unread:{apptown:0,other:0}},overcommitted:true}}
const health = (stateLabel: string) => ({automatic:true, stale:false, stateLabel, checked:stateLabel !== 'not set up', checkedAt:stateLabel === 'not set up' ? undefined : at, ok:stateLabel === 'passed',detail:stateLabel === 'passed' ? 'Connection answered.' : stateLabel === 'not set up' ? 'Infisical is not set up. An administrator adds its credentials.' : 'Connection refused. Check credentials.'})
const entry = (name: string, capability: string, provider: string, state: string) => ({id:name, name, capability, provider, health:health(state), config:{}, testable:true, usedBy:{projects: state === 'failed' ? [{slug:'calendar',name:'Household calendar'}]:[],deployments:[]}})
const attention = {items:[{key:'integrations/registry',title:'Registry · Household calendar',detail:'The connection failed. Ask an administrator to check its credentials.',href:'/integrations/registry',statusLabel:'waiting',observedAt:at,stale:false,severity:'warning'},{key:'checks/calendar',count:2,title:'Household calendar · 2 confirmed problems',detail:'Review and fix the confirmed app checks.',href:'/conformance/projects/calendar',statusLabel:'waiting',observedAt:at,stale:false,severity:'warning'}],sources:[{key:'checks',name:'App checks',href:'/conformance',nextStep:'Review app checks.',unknown:false,stale:false,observedAt:at},{key:'deployments/calendar',name:'Household calendar · deployments',href:'/projects/calendar/settings/deployments',nextStep:'Open deployment diagnostics.',unknown:false,stale:false,observedAt:at},{key:'protection/calendar',name:'Household calendar · backups',href:'/projects/calendar/settings/deployments',nextStep:'Review backup settings.',unknown:false,stale:false,observedAt:at}]}
const responses: Record<string, unknown> = {'/auth/me':{mode:'open',user:'reader'}, '/authz/me':{instance:{}},'/attention':attention,'/targets/attached-host/capacity':capacity,'/integrations':{integrations:[entry('infisical','secrets','infisical','not set up'),entry('registry','container-registry','direct','failed'),{...entry('gitlab','scm','gitlab','passed'),health:{...health('passed'),stale:true,checkedAt:oldAt}},...Array.from({length:13},(_,i)=>({...entry(`packages-${i+1}`,'registry','direct','unknown'),health:{automatic:false,stateLabel:'unknown',checked:false,ok:false,detail:'No automatic check.'}})),...Array.from({length:20},(_,i)=>({...entry(`scanner-${i+1}`,'scanner','docker','unknown'),health:{automatic:false,stateLabel:'unknown',checked:false,ok:false,detail:'No automatic check.'}}))]},'/integrations/catalog':{catalog:[]},'/projects/calendar/findings':{sources:[{binding:'osv',at,unreadable:'SARIF: invalid character after top-level value'}],findings:[],statusLabel:'unknown',summary:{actionable:0},repo:{}}}
window.fetch = async (input) => new Response(JSON.stringify(responses[String(input).replace(/^.*\/api\/v1/,'')] || {}), {status:200,headers:{'Content-Type':'application/json'}})
const resources = {target:'attached-host',kind:'docker-host',live:true, resources:['volume','network','image','container'].map(kind => ({kind,id:kind,name:kind+'-calendar',status: kind === 'container' ? 'running' : 'present',statusLabel:kind === 'container' ? 'running' : 'unknown',record:'recorded',hosts:[{project:'calendar',deployment:'main'}]}))}
const invalidatedResources = {...resources, live:false, invalidated:true, unknown:false, asOf:at}
const views: Record<string, React.ReactNode> = {attention:<Attention/>,capacity:<><h1>Attached host</h1><Capacity name="attached-host"/></>,resources:<><h1>Resources</h1><ResourceTables t={resources as any} onChanged={()=>{}}/></>,'resources-invalidated':<><h1>Resources</h1><ResourceTables t={invalidatedResources as any} onChanged={()=>{}}/></>,integrations:<Integrations/>,security:<><h1>Security</h1><FindingsTab slug="calendar"/></>}
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={newQueryClient()}><MemoryRouter><TopBar me={{mode:'open',user:'reader'}} narrow={innerWidth < 700}/><main style={{maxWidth:1100,margin:'70px auto 0',padding:16}}><p className="muted small">Release recorded <time>{fmtWhen('2026-10-30T06:26:00Z')}</time></p>{views[params.get('view') || 'attention']}</main></MemoryRouter></QueryClientProvider>)
