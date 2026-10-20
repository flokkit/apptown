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
  would no longer cover one), **could not assess** (with why), **no new
  failures**, or **not evaluated**. Recommend the version from there, or tick projects and
  **Adopt** to open one adoption Change per project with exactly what was
  assessed (or `apptown whatif adopt <id> -p <project>[,<project>…]`); each
  Change says which what-if it came from, and a project whose adoption moved
  since is refused (run the what-if again). Recommending from the what-if
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
