import { Link } from 'react-router-dom'
import { Tabs } from '../../Tabs'

const PAGES = [['overview', 'Overview', ''], ['resources', 'Resources', '/resources'], ['capacity', 'Capacity', '/capacity'], ['history', 'History', '/history']] as const
export type TargetTab = typeof PAGES[number][0]

// The four pages about one target, the current one marked.
export function TargetTabs({ name, current }: { name: string; current: TargetTab }) {
  return <nav aria-label="Target pages"><Tabs className="tabs mt-3">
    {PAGES.map(([key, label, path]) => <Link key={key} className={'tab' + (key === current ? ' active' : '')} aria-current={key === current ? 'page' : undefined} to={`/targets/${name}${path}`}>{label}</Link>)}
  </Tabs></nav>
}
