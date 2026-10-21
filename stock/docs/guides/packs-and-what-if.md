# Packs, versions and what-if

A pack is a way of working: workflows, guidance, policies and checks that
projects adopt. They are under **Ways of working → Packs**.

- **Stop offering** a pack and new projects cannot pick it; projects on it
  keep it. Stock packs cannot be deleted. To change a stock pack, **Include
  in a new pack** and change your copy.
- Saving a pack makes a new version. On the pack's page, **Recommend** a
  version to make it the one new projects get and the one others are
  offered (or `apptown pack recommend <pack> <version>`; `apptown pack versions
  <pack>` lists them). No project moves until its owner adopts the version.
  A new AppTown release publishes its stock packs' new versions without
  recommending them; AppTown's start-up log names each one waiting
  (`stock pack <name>: vN published, vM recommended`).
- Before recommending, run **What-if** on that version (or `apptown whatif
  <pack> <version>`): by default it runs on every eligible project, one on
  an earlier version of the pack, or on projects you choose (on the pack,
  or not yet on it, with `-p <project>[,<project>…]`). Its checks run on
  each project's main branch beside what it adopted. Its page has one row
  per project: **new failures** (by check, including an exception that
  would no longer cover one), **could not assess**, **no new failures**, or
  **not evaluated**; beside that, a score (how many checks pass, fail,
  could not be assessed and why, and what would make them assessable) and
  the rules the version moves. A rule taken to an **older version** or
  **removed** is marked **moves back**: going back is allowed, never
  silent, and the adoption Change says it too. The top of the page lists the
  checks that could not be assessed across all the projects, so a check
  that never can be stands out. Workflows the pack does not pin keep the
  version each project is on unless you tick **Also move each project's
  other workflows to the newest version** (`--advance-workflows`); that is
  how a project on only the AppTown pack moves its workflows forward.
  Moving one pack alone keeps a project's other pack at the version it
  recorded, so moving the AppTown pack can take the rules of a project's
  own pack back (and the other way round). Tick **Also move each project's
  own pack to its newest version** (`--own-packs newest`, or name them with
  `--also <pack@version,…>`) to move both together: the versions are listed
  before you run it and recorded with the what-if, and they apply only to
  projects already on those packs. Eligible projects then include those
  current on this pack but behind on their own.
  Every what-if is listed under **Conformance → What-ifs**. Recommend the
  version from there, or tick projects and
  **Adopt** to open one adoption Change per project with exactly what was
  assessed (or `apptown whatif adopt <id> -p <project>[,<project>…]`). Each
  Change starts its working run (quick-change, or implement) at once: the
  run makes the new checks hold, or passes with nothing to do; the row says
  if a run could not be started and why. Each
  Change says which what-if it came from and what it moves back, and a
  project whose adoption moved since is refused (run the what-if again).
  Tick **accept in advance if it needs no code** (or add
  `--accept-in-advance`) to accept each Change now, in the words each
  project shows: one that needs no code and passes every check, with every
  check passing, lands without asking you again; any other waits for a
  person. A project whose quick-change or landing does not say what such a
  yes means offers none.
  Adopting takes the right to choose a project's way of working, which
  maintainers and owners hold. Recommending from the what-if
  page, or with `apptown pack recommend <pack> <version> --what-if <id>`,
  records the what-if on the recommendation it makes; naming a what-if for
  the version already recommended is refused, as nothing would change. Model
  rubrics cost money, so they run only if you include them with a spend
  limit (on the page, or `--rubrics <dollars>`); spend is read before each
  project, and once it reaches the limit no more are sent, so one project's
  calls may pass it. It never changes a project's standing. `apptown whatif show <id>`
  prints one again.
- To see whether work done under the version comes out better, start an
  **Eval** of it (or `apptown eval start --pack <name@version>`): finished
  Changes are re-run in throwaway copies as their projects run now and with
  the version, and the eval page compares them side by side, check by check.
  Evals are read by standards administrators and auditors and started by
  people who manage packs.

How projects measure up against what they adopted is in
[Conformance](/docs/guides/conformance).
