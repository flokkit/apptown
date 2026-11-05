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
  // One sentence: what the page is for. Left out where the help's first
  // paragraph already says it.
  line?: string
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
      <p>New projects start from a pack. Packs marked <span className="badge">{PRODUCT}</span> come with {PRODUCT}. To change one, make your own pack based on it.</p>
    </>,
    guide: 'packs-and-what-if',
  },
  conformance: {
    title: 'Conformance',
    line: 'Check whether each project still follows the ways of working it adopted, and fix what it doesn\'t.',
    help: <p>A review reads each project's accepted code, its <code>main</code> branch, and changes nothing. A project's standing says what a person needs to do about it. A fix is made as a change in each project, reviewed like any other.</p>,
    guide: 'conformance',
  },
  deployments: {
    title: 'Deployments',
    line: 'Where your apps run.',
    help: <p>A deployed project has a deployment of its <code>main</code> branch, which every change that lands updates. A change can have a preview while it is worked on, and a project can be released to more deployments.</p>,
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
  integrations: {
    title: 'Integrations',
    help: <>
      <p>An <strong>integration</strong> is a named connection to an outside service, such as GitLab, Infisical or a model provider. Its settings hold the names of secrets, never their values.</p>
      <ul>
        <li><strong>Source control:</strong> where project code lives.</li>
        <li><strong>Executors:</strong> where workflow steps run.</li>
        <li><strong>Model providers and profiles:</strong> the models agents use.</li>
        <li><strong>Deploy connectors and cloud accounts:</strong> how {PRODUCT} reaches where apps run.</li>
        <li><strong>Scanners:</strong> checks run against a project's code.</li>
      </ul>
      <p>Each says whether it is working now. Test connection asks it again. What projects attach is under Services.</p>
    </>,
    guide: 'monitoring',
  },
  services: {
    title: 'Services',
    help: <>
      <p>Services are the systems an app connects to, such as a database, AI models, error reporting or your organisation's own systems. Some, like a database, come with wherever the app is deployed. Connect the others here; each deployment and preview gets its own credentials.</p>
      <p>Projects then choose which of these to use, under each project's Settings › Services.</p>
    </>,
    guide: 'services-and-data',
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
    line: "Try a new model profile or way of working on changes you've already made, and see whether it does better.",
    help: <p>An eval re-runs finished changes in throwaway copies. One copy runs as its project runs now. The other uses the model profile or pack version you choose. You read the two results side by side. An eval approves nothing.</p>,
    guide: 'packs-and-what-if',
  },
  projectOverview: {
    title: 'Overview',
    help: <>
      <p>The overview shows this project's open changes, recent activity and deployments.</p>
      <p>Open a change to see its work and what it needs next. Start a change to ask for new work. Each deployment shows its current state and address.</p>
    </>,
    guide: 'changes-and-main',
  },
  projectSecurity: {
    title: 'Security',
    help: <>
      <p>Security shows the latest scan results for this project's accepted code.</p>
      <p>Findings lists problems the scans found. Suppressions lists recorded decisions to accept findings for a time. A missing or unreadable report leaves checks incomplete.</p>
    </>,
    guide: 'monitoring',
  },
  projectSettings: {
    title: 'Settings',
    help: <>
      <p>Settings are the choices for this project's setup and who can work on it.</p>
      <p>Choose a page to edit its details, way of working, services, deployments, secrets or members, or to read decisions and model usage.</p>
    </>,
    guide: 'project-settings',
  },
} satisfies Record<string, PageHelp>

export type HelpPage = keyof typeof PAGE_HELP

// Pages inside an area say what they themselves are for; the area's help
// would only repeat the area. Keyed by area and page title.
export const SUB_HELP: Partial<Record<HelpPage, Record<string, { body: ReactNode; guide: string }>>> = {
  projectSecurity: {
    Findings: { guide: 'monitoring', body: <>
      <p>Findings are problems reported by security scans of this project's accepted code.</p>
      <p>Read the latest scan results and open a finding to fix it or record a suppression. If main has changed since the scan, open a change to scan the latest code. Missing or unreadable reports leave checks incomplete.</p>
    </> },
    Suppressions: { guide: 'glossary', body: <>
      <p>Suppressions are recorded decisions to accept security findings for a time.</p>
      <p>Create one from Findings, with a reason and an expiry. Each matches findings by fingerprint or identifier and package. When it expires, the finding fails the gate again. Revoke one here to make the finding actionable immediately, or export the records as OpenVEX.</p>
    </> },
  },
  projectSettings: {
    General: { guide: 'project-settings', body: <>
      <p>General holds the project's name, address, icon, group and description.</p>
      <p>Edit these details here. You can also download the code, import a branch as a change, or delete the project. The code download includes the default branch and tags; import it to start another project.</p>
    </> },
    'Way of working': { guide: 'packs-and-what-if', body: <>
      <p>A way of working is the set of packs this project has adopted, with their workflows, guidance, policies and checks.</p>
      <p>Read what the project uses, compare versions and choose updates. Adopting a pack selects its standards. A conformance review checks whether the app meets them.</p>
    </> },
    Services: { guide: 'services-and-data', body: <>
      <p>Services are the systems this app uses, such as a database, AI models or error reporting.</p>
      <p>Changing a declared service opens a change to update the app's contract for approval. Connections attach outside services to the project. Each deployment and preview gets its own credentials.</p>
    </> },
    Deployments: { guide: 'container-lifecycle', body: <>
      <p>A deployment is a named copy of this app on a deployment target.</p>
      <p>See where the app runs, add a deployment on a target offered to this project, and choose where Release sends it. Open a deployment for its resources, backups and actions.</p>
      <p>Old resources lists data without a final copy and resources that could not be deleted. Delete them at the provider, then confirm here to free the deployment name.</p>
    </> },
    Secrets: { guide: 'secrets', body: <>
      <p>Secrets are values your app needs but should not be part of the code: passwords, API keys, signing keys. {PRODUCT} keeps them and gives them to the app when it runs.</p>
      <p>Project secrets are declared in <code>secretspec.toml</code>.</p>
      <p>Entered values will not be shown again.</p>
    </> },
    Members: { guide: 'people-and-roles', body: <>
      <p>Members are the people and groups with roles on this project.</p>
      <p>A contributor opens changes and hands them off. A maintainer also lands them and changes the project's setup. An approver says yes at approval steps. An owner manages the project and its members.</p>
      <p>Add or remove roles here, and choose whether another person must approve the author's change. The Approval section shows who can approve and who can land changes.</p>
    </> },
    Decisions: { guide: 'glossary', body: <>
      <p>Decisions are records of what this project chose and why.</p>
      <p>Open a record to read it and its status. Start a draft with a title and optional tags to record a new decision.</p>
    </> },
    Usage: { guide: 'monitoring', body: <>
      <p>Usage is this project's AI model use and estimated cost.</p>
      <p>Choose a period to see tokens and cost by model, daily use and time spent in workflow steps. Workflow use comes from runs; interactive use comes from chats. Unpriced use is shown separately.</p>
    </> },
  },
  deployments: {
    Departures: { guide: 'container-lifecycle', body: <>
      <p>See every deploy, newest first: which app went where, when, and whether it came up.</p>
      <p>A deploy happens when a change lands, or when a project is released to another deployment.</p>
    </> },
    Apps: { guide: 'how-apps-run', body: <>
      <p>See each app and where it runs: its deployments, whether each is up, and its address.</p>
      <p>A project that has never been deployed is listed as not deployed.</p>
    </> },
    Infrastructure: { guide: 'deployment-targets', body: <>
      <p>See what runs on each deployment target and how full it is, and anything on a target that {PRODUCT} did not expect.</p>
      <p>Setting targets up, and who may use each, is under Integrations › Deployment targets.</p>
    </> },
    Backups: { guide: 'deployment-backups', body: <>
      <p>See each deployment's latest backups, and restore from them. A backup copies an app's data, such as its database.</p>
      <p>With a backup store attached, copies are kept off this machine; without one they stay on {PRODUCT}'s own disk, and losing that disk loses them.</p>
    </> },
  },
  waysOfWorking: {
    Packs: { guide: 'packs-and-what-if', body: <>
      <p>Choose the packs projects can start from. A pack bundles a stack with the workflows, guidance and policies that go with it.</p>
      <p>Packs marked {PRODUCT} come with {PRODUCT}; to change one, make your own pack based on it.</p>
    </> },
    Workflows: { guide: 'ways-of-working-formats', body: <>
      <p>See the steps a change goes through, from request to release: who builds it, which checks run and who approves it.</p>
      <p>A project gets its workflows from its packs; a run keeps the version it started with.</p>
    </> },
    Guidance: { guide: 'ways-of-working-formats', body: <>
      <p>Read and write the instructions coding agents follow. Some guidance also carries checks that run when a change lands and in reviews.</p>
    </> },
    Policies: { guide: 'ways-of-working-formats', body: <>
      <p>Set rules every workflow must follow, such as "a deploy needs a scan first". A policy is checked whenever a workflow is saved.</p>
    </> },
  },
}

// sub names the page inside an area the menu opens as a drawer: the title is
// the page ("Departures") with its area above it ("Deployments"). A sub-page
// that is about one thing of its own (a kind of integration) passes its own
// help in place of the area's.
// line is the page's one-sentence subtitle, kept with its title.
export function PageHead({ page, actions, sub, help, line, title }: { page: HelpPage; title?: ReactNode; actions?: ReactNode; sub?: string; help?: { body: ReactNode; guide: string }; line?: ReactNode }) {
  const h: PageHelp = PAGE_HELP[page]
  help = help ?? (sub ? SUB_HELP[page]?.[sub] : undefined)
  return (
    <div className="page-head">
      {sub && <div className="page-area">{h.title}</div>}
      <div className="page-head-row">
        <h1>{title ?? sub ?? h.title}</h1>
        {help ? <HelpButton about={sub ?? h.title} guide={help.guide}>{help.body}</HelpButton>
          : <HelpButton about={h.title} guide={h.guide}>{h.line && <p>{h.line}</p>}{h.help}</HelpButton>}
        {actions && <><span className="grow" />{actions}</>}
      </div>
      {line && <p className="page-head-line">{line}</p>}
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
