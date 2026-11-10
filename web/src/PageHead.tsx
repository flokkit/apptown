import { BetaChip, BetaPage } from './features'
import { ReactNode, useCallback, useContext, useId, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { PRODUCT } from './brand'
import { Modal, useDismiss } from './Modal'
import { useNarrow } from './chrome'

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
      <p>New projects start from a pack. Packs marked <span className="badge">yours</span> were made here; the rest come with {PRODUCT}. To change one of those, make your own pack based on it.</p>
    </>,
    guide: 'packs-and-what-if',
  },
  conformance: {
    title: 'Conformance',
    line: 'Check whether each project still follows the ways of working it adopted, and fix what it doesn\'t.',
    help: <p>A review reads each project's accepted code, its <code>main</code> branch, and changes nothing. A project's standing says what a person needs to do about it. A fix is made as a change in each project, reviewed like any other.</p>,
    guide: 'conformance',
  },
  packUpgrades: {
    title: 'Pack upgrades',
    line: 'Move projects onto newer versions of the packs they adopted, after trying each version first.',
    help: <p>A what-if runs a pack version's checks against projects as they stand, without changing them, and says per project what would newly fail, newly pass or stay the same. From its report you open adoption changes for the projects you choose; each is reviewed like any other change.</p>,
    guide: 'packs-and-what-if',
  },
  deployments: {
    title: 'Deployments',
    line: 'Where your apps run.',
    help: <p>A deployment chooses its release feed: follows main, promotion from another deployment, or by hand. A change can have a preview while it is worked on, and a project can be released to more deployments.</p>,
    guide: 'container-lifecycle',
  },
  projectDeployments: {
    title: 'Deployments',
    guide: 'container-lifecycle',
    help: <><p>A deployment is a named copy of this app on a target. Choose New deployment to select its target and release feed: Follows main, promotion from another deployment, or By hand.</p><p>The release preference is here. Open a deployment to change its settings and review its releases. Old resources link to their target, where they are settled.</p></>,
  },
  releases: {
    title: 'Releases',
    help: <p>Recent releases across projects, including those waiting for approval. Open a release to read its outcome and approval record.</p>,
    guide: 'container-lifecycle',
  },
  backups: {
    title: 'Backups',
    help: <p>Saved data across all deployments, newest first. Each copy records when it was taken, its contents and where it is kept.</p>,
    guide: 'container-lifecycle',
  },
  infrastructure: {
    title: 'Targets',
    line: 'The places your apps run.',
    help: <>
      <p>A deployment target is a place deployments run, such as a Docker host, a Portainer environment or a DigitalOcean account. Each target's page shows what runs there and how full it is.</p>
      <p>Previews run on the <Link to="/integrations/executor">executor</Link>.</p>
    </>,
    guide: 'deployment-targets',
  },
  integrations: {
    title: 'Integrations',
    help: <>
      <p>An <strong>integration</strong> is a named connection to an outside service, such as GitLab, Infisical or a model provider. Its settings hold the names of secrets, never their values.</p>
      <ul>
        <li><strong>Source control:</strong> where project code lives.</li>
        <li><strong>Executor:</strong> where workflow steps run.</li>
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
  features: {
    title: 'Features',
    line: `Turn parts of ${PRODUCT} on or off for everyone.`,
    help: <p>The ? beside each switch says what turning it off changes. A greyed switch needs another one on first.</p>,
    guide: 'features',
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
      <p>The overview shows this project's recent activity and deployments.</p>
      <p>Open Changes to see work in progress or ask for new work. Each deployment shows its current state and address.</p>
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
  projectMemory: {
    title: 'Memory',
    help: <p>Memory is what agents carry into their work on this project: its notes and records of decisions.</p>,
    guide: 'project-notes',
  },
  projectSettings: {
    title: 'Settings',
    help: <>
      <p>Settings are the choices for this project's setup and who can work on it.</p>
      <p>Choose a page to edit its details, way of working, services, deployments, secrets or members, or to read model usage.</p>
    </>,
    guide: 'project-settings',
  },
} satisfies Record<string, PageHelp>

export type HelpPage = keyof typeof PAGE_HELP

// Pages inside an area say what they themselves are for; the area's help
// would only repeat the area. Keyed by area and page title.
export const SUB_HELP: Partial<Record<HelpPage, Record<string, { body: ReactNode; guide: string }>>> = {
  people: {
    Accounts: { guide: 'people-and-roles', body: <>
      <p>Everyone who can sign in to {PRODUCT}.</p>
      <p>Invite someone to add them. The ⋯ on a row resets a password or turns the account off.</p>
    </> },
    // One account's page: its own actions, not the list's.
    Account: { guide: 'people-and-roles', body: <>
      <p>One person's account and the groups they are in.</p>
      <p>The ⋯ resets their password or turns the account off.</p>
    </> },
    Groups: { guide: 'people-and-roles', body: <>
      <p>A group is a named set of people. Give a group a role and everyone in it gets that access.</p>
      <p>A workflow can ask any member of a group to approve: put <code>group:name</code> in its approvers. The record keeps the name of whoever approved.</p>
    </> },
    Roles: { guide: 'people-and-roles', body: <>
      <p>A role is what someone may do across {PRODUCT}. Click a role to see what it allows.</p>
      <p>Administrators can act in every project and can approve their own changes, unless a project asks for someone else's approval. Roles inside one project are in that project's Settings › Members.</p>
    </> },
    'Sign-in': { guide: 'people-and-roles', body: <p>How people sign in, and the networks administrators may sign in from.</p> },
    'Agents and tokens': { guide: 'people-and-roles', body: <p>Coding agents connected to {PRODUCT}, and the tokens they use. Disconnecting one stops it at once.</p> },
  },
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
  projectMemory: {
    Notes: { guide: 'project-notes', body: <>
      <p><code>PROJECT-NOTES.md</code> records durable facts and preferences about this project. Agents read it in every session.</p>
      <p>Entries change through changes, with an author and change number. Notes are claims, never evidence.</p>
    </> },
    Decisions: { guide: 'glossary', body: <>
      <p>Decisions are records of what this project chose and why.</p>
      <p>Open a record to read it and its status. Start a draft with a title and optional tags to record a new decision.</p>
    </> },
  },
  projectSettings: {
    General: { guide: 'project-settings', body: <>
      <p>General holds the project's name, address, icon, group and description.</p>
      <p>Edit these details here. You can also download the code or delete the project. The code download includes the default branch and tags; import it to start another project.</p>
    </> },
    Models: { guide: 'architecture', body: <>
      <p>A model profile chooses which AI model does each task for this project.</p>
      <p>Read the model choices and select another profile here. An unset task follows the profile’s basic choices, then standard’s models. Changing a profile’s task choices is under Integrations → Model profiles.</p>
    </> },
    'Way of working': { guide: 'packs-and-what-if', body: <>
      <p>A way of working is the set of packs this project has adopted, with their workflows, guidance, policies and checks.</p>
      <p>Read what the project uses, compare versions and choose updates. Adopting a pack selects its standards. A conformance review checks whether the app meets them.</p>
    </> },
    Services: { guide: 'services-and-data', body: <>
      <p>Services are the systems this app uses, such as a database, AI models or error reporting.</p>
      <p>Changing a declared service opens a change to update the app's contract for approval. Connections attach outside services to the project. Each deployment and preview gets its own credentials.</p>
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
    Usage: { guide: 'monitoring', body: <>
      <p>Usage is this project's AI model use and estimated cost.</p>
      <p>Choose a period to see tokens and cost by model, daily use and time spent in workflow steps. Workflow use comes from runs; interactive use comes from chats. Unpriced use is shown separately.</p>
    </> },
  },
  waysOfWorking: {
    Packs: { guide: 'packs-and-what-if', body: <>
      <p>Choose the packs projects can start from. A pack bundles a stack with the workflows, guidance and policies that go with it.</p>
      <p>Packs marked yours were made here; the rest come with {PRODUCT}. To change one of those, make your own pack based on it.</p>
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
// the page ("Releases") with its area above it ("Deployments"). A sub-page
// that is about one thing of its own (a kind of integration) passes its own
// help in place of the area's.
// line is the page's one-sentence subtitle, kept with its title.
export function PageHead({ page, actions, sub, help, line, title }: { page: HelpPage; title?: ReactNode; actions?: ReactNode; sub?: string; help?: { body: ReactNode; guide: string }; line?: ReactNode }) {
  const h: PageHelp = PAGE_HELP[page]
  const beta = useContext(BetaPage)
  help = help ?? (sub ? SUB_HELP[page]?.[sub] : undefined)
  return (
    <div className="page-head">
      <div className="page-head-row">
        {/* The title and its "?" stay together; actions wrap as a block. */}
        <span className="page-head-title">
          <h1>{title ?? sub ?? h.title}</h1>
          {help ? <HelpButton about={sub ?? h.title} guide={help.guide}>{help.body}</HelpButton>
            : <HelpButton about={h.title} guide={h.guide}>{h.line && <p>{h.line}</p>}{h.help}</HelpButton>}
          {beta && <BetaChip />}
        </span>
        {/* The actions keep to the right, on the title's line or, wrapped, on their own. */}
        {actions && <span className="page-head-actions">{actions}</span>}
      </div>
      {line && <p className="page-head-line">{line}</p>}
    </div>
  )
}

// The "?" and its popover: a disclosure, so the button says whether it is
// open and the popover follows it in the page, where Tab reaches it next.
// Escape and a click outside close it, as the split button's menu does.
// On a phone the popover would lie over the very list or form it explains,
// so there the "?" opens a dialog sized to its text, with a close button.
// The dialog is portalled out of the "?"'s box, so its own dismissal is the
// only one: the box's would take a press on Read more for a click outside.
export function HelpButton({ about, guide, children }: { about: string; guide: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const narrow = useNarrow()
  const box = useRef<HTMLSpanElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  useDismiss(box, close, open && !narrow)
  // Escape from inside the popover would leave focus on a node that is
  // about to go; hand it back to the "?". A click outside leaves focus to
  // whatever was clicked.
  const onKeyDown = (e: React.KeyboardEvent) => { if (e.key === 'Escape') button.current?.focus() }
  const body = <>{children}<Link to={`/docs/guides/${guide}`} onClick={close}>Read more</Link></>
  return (
    <span ref={box} className="page-help" onKeyDown={onKeyDown}>
      <button ref={button} type="button" className="page-help-q" aria-expanded={open} aria-controls={open ? id : undefined}
        aria-haspopup={narrow ? 'dialog' : undefined}
        aria-label={`About ${about}`} title="What's this?" onClick={() => setOpen((o) => !o)}>?</button>
      {open && (narrow
        ? <Modal fit title={`About ${about}`} onClose={close}><div id={id} className="page-help-text">{body}</div></Modal>
        : <div id={id} className="page-help-pop" role="region" aria-label={`About ${about}`}>{body}</div>)}
    </span>
  )
}
