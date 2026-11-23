import { useParams } from 'react-router-dom'
import { useGet } from '../../data'
import { PageBar } from '../../PageBar'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { deploymentName } from '../../deploymentName'
import { ProtectionLines } from '../DataProtection'
import type { DeploymentView } from '../../wire.gen'
import '../../overview-line.css'
import './settings/deployments.css'

// DeploymentBackupsPage is one deployment's backups, one tap from its
// Status tab: each item's newest copy, where copies are kept, restore tests
// and rehearsals, what the copies do not cover, and the doors to copy,
// check and test now (ProtectionLines, the owner's account unchanged).
export function DeploymentBackupsPage() {
  const { slug = '', name = '' } = useParams()
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const env = q.data?.find(e => e.name === name)
  return <>
    <PageBar title={env ? <>Backups <span className="title-context">{deploymentName(env)}</span></> : 'Backups'} />
    {env ? <><StaleNote q={q} /><div className="card deploy-entry"><div className="deploy-lines"><ProtectionLines slug={slug} name={name} /></div></div></> : <ReadStatus q={q} what="the deployment" />}
  </>
}
