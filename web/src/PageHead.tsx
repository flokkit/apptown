import { ReactNode, useCallback, useId, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { PRODUCT } from './brand'
import { useDismiss } from './Modal'

// A page's title, one short line under it, and a "?" beside the title that
// opens the longer explanation with a link to its guide. Pages show facts;
// the explanation is one click away instead of a paragraph above every page
// (design/writing.md). The texts live in PAGE_HELP so they read as one set
// and a test can check each names a shipped guide.

export type PageHelp = {
  title: string
  // One sentence: what the page is for.
  line: string
  // What a newcomer needs to read the page, in short sentences.
  help: ReactNode
  // A guide slug under stock/docs/guides, opened at /docs/guides/<slug>.
  guide: string
}

export const PAGE_HELP = {
  waysOfWorking: {
    title: 'Ways of working',
    line: 'The tech stack your apps are built with, and how changes to them get made and checked.',
    help: <>
      <p>Each way of working is a <strong>pack</strong>. A pack sets:</p>
      <ul>
        <li><strong>The stack:</strong> language, framework, database and build.</li>
        <li><strong>Workflows:</strong> the steps from request to release.</li>
        <li><strong>Guidance:</strong> instructions the coding agents read.</li>
        <li><strong>Policies:</strong> rules every change has to pass.</li>
      </ul>
      <p>New projects start from a pack. Packs marked <span className="chip">{PRODUCT}</span> come with {PRODUCT}. To change one, make your own pack based on it.</p>
    </>,
    guide: 'packs-and-what-if',
  },
  conformance: {
    title: 'Conformance',
    line: 'Whether each app meets the ways of working it adopted.',
    help: <p>A review checks each project's committed <code>main</code> branch. It reads the code and changes nothing. A project's standing says what a person needs to do about it.</p>,
    guide: 'conformance',
  },
  deployments: {
    title: 'Deployments',
    line: 'Where your apps run.',
    help: <p>Each project has a deployment of its <code>main</code> branch. Every change that lands updates it. A change can have a preview while it is worked on. A project can also be released to more deployments.</p>,
    guide: 'container-lifecycle',
  },
  infrastructure: {
    title: 'Infrastructure',
    line: `The deployment targets apps run on, and ${PRODUCT}'s own background jobs.`,
    help: <>
      <p>A deployment target is a place deployments run, such as a Docker host, a Portainer environment or a DigitalOcean account. Each target's page shows what runs there and how full it is.</p>
      <p>The job queue is {PRODUCT}'s background work, such as backups, inventories and workflow follow-ups. Jobs that are safe to repeat retry by themselves. A stopped or failed job may need someone to run it again.</p>
    </>,
    guide: 'deployment-targets',
  },
  people: {
    title: 'People',
    line: `Accounts and groups on this ${PRODUCT}, and the roles they hold.`,
    help: <p>A group can be named as an approver. Write <code>group:name</code> in a workflow's approver list. Any member's approval counts. The record keeps the name of the person who approved.</p>,
    guide: 'people-and-roles',
  },
  usage: {
    title: 'Usage',
    line: 'AI model use and cost, by model and project.',
    help: <p>It counts the projects you can read. If you can read all of {PRODUCT}, it counts all of it. Workflow use comes from runs. Interactive use comes from chats. Costs are the per-message prices pi, the agent tool, reports. Local models cost $0.</p>,
    guide: 'monitoring',
  },
  evals: {
    title: 'Evals',
    line: 'Re-run finished changes with another model or pack version, and compare the results.',
    help: <p>Each finished change is re-run in throwaway copies. One copy runs as its project runs now. The other uses the model profile or pack version you choose. You read the two results side by side. An eval approves nothing.</p>,
    guide: 'packs-and-what-if',
  },
} satisfies Record<string, PageHelp>

export type HelpPage = keyof typeof PAGE_HELP

export function PageHead({ page, actions }: { page: HelpPage; actions?: ReactNode }) {
  const h: PageHelp = PAGE_HELP[page]
  return (
    <div className="page-head">
      <div className="page-head-row">
        <h1>{h.title}</h1>
        <HelpButton about={h.title} guide={h.guide}><p>{h.line}</p>{h.help}</HelpButton>
        {actions && <><span style={{ flex: 1 }} />{actions}</>}
      </div>
    </div>
  )
}

// The "?" and its popover: a disclosure, so the button says whether it is
// open and the popover follows it in the page, where Tab reaches it next.
// Escape and a click outside close it, as the split button's menu does.
export function HelpButton({ about, guide, children }: { about: string; guide: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLSpanElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  useDismiss(box, close, open)
  // Escape from inside the popover would leave focus on a node that is
  // about to go; hand it back to the "?". A click outside leaves focus to
  // whatever was clicked.
  const onKeyDown = (e: React.KeyboardEvent) => { if (e.key === 'Escape') button.current?.focus() }
  return (
    <span ref={box} className="page-help" onKeyDown={onKeyDown}>
      <button ref={button} type="button" className="page-help-q" aria-expanded={open} aria-controls={id}
        aria-label={`About ${about}`} title="What's this?" onClick={() => setOpen((o) => !o)}>?</button>
      {open && (
        <div id={id} className="page-help-pop" role="region" aria-label={`About ${about}`}>
          {children}
          <Link to={`/docs/guides/${guide}`}>Read more</Link>
        </div>
      )}
    </span>
  )
}
