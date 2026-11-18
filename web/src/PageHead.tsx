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
  // What a newcomer needs to read the page. With the line, at most two
  // sentences: the "?" is a reminder, and anything longer belongs in the
  // guide its "Read more" opens (a test holds every entry to this).
  help: ReactNode
  // A guide slug under stock/docs/guides, opened at /docs/guides/<slug>.
  guide: string
}

export const PAGE_HELP = {
  // The help of Standards' record pages (a project's standing, a review, a
  // remediation); each Maintenance page has its own under SUB_HELP.
  maintenance: {
    title: 'Maintenance',
    help: <p>Maintenance keeps projects up to date and checks that they still follow the ways of working they adopted. Every fix is made as an ordinary change, reviewed like any other.</p>,
    guide: 'conformance',
  },
  notifications: { title: 'Notifications', line: 'Attention items sent by webhook or email.', help: <p>Warnings arrive within about two minutes; other items arrive in a daily digest at each channel’s UTC hour.</p>, guide: 'notifications' },
  waysOfWorking: {
    title: 'Ways of working',
    line: 'The tech stack your apps are built with, and how changes to them get made and checked.',
    help: <p>Each way of working is a <strong>pack</strong>: a stack with the workflows, guidance and policies that go with it.</p>,
    guide: 'packs-and-what-if',
  },
  deployments: {
    title: 'All deployments',
    line: 'Where your apps run.',
    help: <p>A deployment chooses its release feed: follows main, promotion from another deployment, or by hand.</p>,
    guide: 'container-lifecycle',
  },
  projectDeployments: {
    title: 'Deployments',
    guide: 'container-lifecycle',
    help: <p>A deployment is a named copy of this app on a target, with a release feed: Follows main, promotion from another deployment, or By hand. Open one to change its settings and read its releases.</p>,
  },
  releases: {
    title: 'All releases',
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
    help: <p>A deployment target is a place deployments run, such as a Docker host, a Portainer environment or a DigitalOcean account. Previews, the trial copies of an app made while a change is worked on, run on the <Link to="/integrations/executor">executor</Link> instead.</p>,
    guide: 'deployment-targets',
  },
  integrations: {
    title: 'Integrations',
    help: <p>An <strong>integration</strong> is a named connection to an outside service, such as GitLab, Infisical or a model provider; its settings hold the names of secrets, never their values. Each says whether it is working now, and Test connection asks it again.</p>,
    guide: 'monitoring',
  },
  services: {
    title: 'Services',
    help: <p>Services are the systems an app connects to, such as a database, AI models or error reporting; each deployment and preview gets its own credentials. Projects choose which to use under each project's Settings › Services.</p>,
    guide: 'services-and-data',
  },
  people: {
    title: 'People',
    line: `Accounts and groups on this ${PRODUCT}, and the roles they hold.`,
    help: <p>Open a page to invite people, form groups and grant roles.</p>,
    guide: 'people-and-roles',
  },
  features: {
    title: 'Features',
    line: `Turn parts of ${PRODUCT} on or off for everyone.`,
    help: <p>The ? beside each switch says what turning it off changes.</p>,
    guide: 'features',
  },
  usage: {
    title: 'Usage',
    line: 'AI model use and cost, by model and project.',
    help: <p>Costs are estimates, not your provider's bill; use with no known price shows as not priced.</p>,
    guide: 'monitoring',
  },
  evals: {
    title: 'Evals',
    line: "Try a new model profile or way of working on changes you've already made, and see whether it does better.",
    help: <p>An eval re-runs finished changes in throwaway copies, one as the project runs now and one with the model profile or pack version you choose, and approves nothing.</p>,
    guide: 'packs-and-what-if',
  },
  projectOverview: {
    title: 'Overview',
    help: <p>This project's recent activity and deployments, with each deployment's state and address. Open Changes to see work in progress or ask for new work.</p>,
    guide: 'changes-and-main',
  },
  projectSecurity: {
    title: 'Security',
    help: <p>The latest scan results for this project's accepted code. A missing or unreadable report leaves checks incomplete.</p>,
    guide: 'monitoring',
  },
  projectMemory: {
    title: 'Memory',
    help: <p>Memory is what helpers carry into their work on this project: its notes and records of decisions.</p>,
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
    Accounts: { guide: 'people-and-roles', body: <p>Everyone who can sign in to {PRODUCT}. Invite someone to add them; the ⋯ on a row resets a password or turns the account off.</p> },
    // One account's page: its own actions, not the list's.
    Account: { guide: 'people-and-roles', body: <>
      <p>One person's account and the groups they are in.</p>
      <p>The ⋯ resets their password or turns the account off.</p>
    </> },
    Groups: { guide: 'people-and-roles', body: <p>A group is a named set of people; give it a role and everyone in it gets that access. A workflow's approval step can name a group instead of a person, and then any member's approval counts.</p> },
    Roles: { guide: 'people-and-roles', body: <p>A role is what someone may do across {PRODUCT}; click one to see what it allows. Roles inside one project are in that project's Settings › Members.</p> },
    'Sign-in': { guide: 'people-and-roles', body: <p>How people sign in, and the networks administrators may sign in from.</p> },
    'Agents and tokens': { guide: 'people-and-roles', body: <p>Coding helpers connected to {PRODUCT}, and the tokens they use. Disconnecting one stops it at once.</p> },
  },
  projectSecurity: {
    'Security findings': { guide: 'monitoring', body: <p>Problems reported by security scans of this project's accepted code. Open a finding to fix it or record a suppression; if main has changed since the scan, open a change to scan the latest code.</p> },
    Suppressions: { guide: 'glossary', body: <p>Recorded decisions to accept security findings for a time, made from Findings with a reason and an expiry. When one expires or is revoked here, its finding fails the security check again.</p> },
  },
  projectMemory: {
    Notes: { guide: 'project-notes', body: <p><code>PROJECT-NOTES.md</code> records durable facts and preferences about this project, and helpers read it in every session. Notes are edited through a change, so every edit has an author and a change number.</p> },
    Decisions: { guide: 'glossary', body: <p>Records of what this project chose and why. Open one to read it and its status, or start a draft with a title and optional tags.</p> },
  },
  projectSettings: {
    General: { guide: 'project-settings', body: <p>The project's name, address, icon, group and description. You can also download the code, with its default branch and tags, or delete the project.</p> },
    Models: { guide: 'architecture', body: <p>A model profile chooses which AI model does each task for this project. A profile's own choices are under Integrations › Model profiles.</p> },
    'Way of working': { guide: 'packs-and-what-if', body: <p>The packs this project has adopted, with their workflows, guidance, policies and checks. Compare versions and choose updates here.</p> },
    Services: { guide: 'services-and-data', body: <p>The systems this app uses, such as a database, AI models or error reporting. Changing a declared service opens a change to the app's contract for approval.</p> },
    Secrets: { guide: 'secrets', body: <p>Values your app needs that should not be in its code, such as passwords and API keys, as its secretspec.toml file lists them. Entered values will not be shown again.</p> },
    Members: { guide: 'people-and-roles', body: <p>The people and groups with roles on this project: contributors, maintainers, approvers and owners. Add or remove roles here, and choose whether another person must approve the author's change.</p> },
    Usage: { guide: 'monitoring', body: <p>This project's AI model use and estimated cost. Choose a period to see tokens and cost by model, daily use and time spent in workflow steps.</p> },
  },
  waysOfWorking: {
    Packs: { guide: 'packs-and-what-if', body: <p>Choose the packs projects can start from. Packs marked yours were made here; to change one that comes with {PRODUCT}, make your own pack based on it.</p> },
    Workflows: { guide: 'ways-of-working-formats', body: <p>The steps a change goes through, from request to release: who builds it, which checks run and who approves it. A project gets its workflows from its packs; a run keeps the version it started with.</p> },
    Guidance: { guide: 'ways-of-working-formats', body: <p>Read and write the instructions coding helpers follow. Some guidance also carries checks that run when a change lands and in reviews.</p> },
    Policies: { guide: 'ways-of-working-formats', body: <p>Set rules every workflow must follow, such as "a deploy needs a scan first". A policy is checked whenever a workflow is saved.</p> },
  },
  // Each Maintenance page says what that page shows.
  maintenance: {
    'Needs you': { guide: 'janitor', body: <p>Janitor work waiting for a person: a change to accept, an update it could not finish, or a held release.</p> },
    Standards: { guide: 'conformance', body: <p>Whether each project still meets the ways of working it adopted. A review reads each project's <code>main</code> branch and changes nothing.</p> },
    Updates: { guide: 'janitor', body: <p>Changes the Janitor has landed, newest first.</p> },
    Settings: { guide: 'who-keeps-what-updated', body: <p>What the Janitor keeps up to date in every project. Each project can choose its own dependency level or turn the Janitor off.</p> },
    'Pack upgrades': { guide: 'packs-and-what-if', body: <p>Projects on an older version of a pack they adopted. A what-if tries a newer version on them without changing anything.</p> },
    Checks: { guide: 'conformance', body: <>
      <p>Every check the projects' adopted packs ask for, with how many projects pass, fail or could not be checked.</p>
      <p>Open a check to see each project's latest result for it.</p>
    </> },
    'this check': { guide: 'conformance', body: <p>This check's result in each project that adopted it, with the commit it was judged on. A fix for a finding is made as a change in that project.</p> },
    Reviews: { guide: 'conformance', body: <>
      <p>Each review read every project's <code>main</code> branch at one time and changed nothing.</p>
      <p>Open one to see the results as they were then.</p>
    </> },
    Remediations: { guide: 'conformance', body: <p>A remediation fixes findings with one change per project, one at a time, each waiting for approval like any other change.</p> },
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
