# A forge on a public gitlab.com group

Setup for a forge's projects in a public gitlab.com group, written 9 October
2026 against GitLab's published documentation. The operator does this setup on
gitlab.com by hand; the forge does not automate it. What the forge writes on
a merge request and its merge commit is in the
[SCM package guide](../../internal/forge/scm/README.md#what-gitlab-is-told).
The plan and its reviews are in

## Setup

1. **Group.** The group's Owner creates a public top-level group. On gitlab.com
   the Owner must verify their identity before creating service accounts.
2. **The forge's account.** Create it as a group *service account*: on the Free
   tier since GitLab 18.11, up to 100 per top-level group, no seat, and no UI
   sign-in. Add it to the group as Maintainer. Give it a personal access token
   with `api` and `write_repository`, and the latest expiry the form accepts
   (365 days unless the instance allows more). Write the rotation date down.
   The forge's secrets:

   ```
   GITLAB_URL=https://gitlab.com
   GITLAB_TOKEN=<the service account's token>
   GITLAB_NAMESPACE_ID=<the group's id>
   ```

3. **Branch protection.** Setting the default branch protection for every
   project in a group needs Premium or Ultimate, so do not rely on it. New
   projects inherit gitlab.com's default, "fully protected": Developers cannot
   push, Maintainers can, and no one can force push. On each project, check
   under Settings → Repository → Protected branches that `main` allows
   Maintainers to merge and to push and merge (a direct landing pushes main),
   with force push off. Give no other account Developer or higher. Readers need
   no membership, because the group is public.
4. **Each project.** The forge creates GitLab projects private. Making one
   public is a publication decision, so a person changes its visibility on
   GitLab. Set the merge method to "merge commit with semi-linear history",
   squash to "do not allow", and leave automatic rebase off. A landing refuses
   other settings (`checkedMergeSettings`). Semi-linear makes a merge commit,
   which carries the forge's trailers; fast-forward makes none, and then only
   the description says what admitted the merge. Leave GitLab's approval button
   and pipeline requirements unused: the forge's gates are the record, and its
   account never approves on GitLab.
5. **Merge requests.** A project gets merge requests only from a landing
   workflow with an `scm/mr` step. Stock `land` has none, so it lands directly
   and pushes main. Use a copy with the step after the checks; the stock policy
   `mr-requires-secrets-scan` asks for a scan before it.

## What this setup does not do

- **Only Maintainers can merge, not only the forge.** Free protects branches by
  role; naming users is a Premium feature. The forge's account is the only
  merger only while it is the only Maintainer. The group's Owner can still
  push, merge or unprotect. The forge detects such a move but cannot prevent
  it: a landing is refused when main moved from its pinned target
  (`LandingMoved`), and the project page reports "main is N commits ahead of
  the forge's record".
- **Agent steps get no GitLab token.** Project access tokens need Premium or
  Ultimate on gitlab.com. A step that declares `scm:` runs without GitLab
  access, and its progress reads "scm access unavailable: <GitLab's refusal>".
  No stock workflow declares `scm:`. The forge's own token is never handed to
  an agent instead, because it is Maintainer on every project. One service
  account per project, with a rotated token, is a possible later affordance;
  it is not built.
- **Names, not identities.** Merge requests and merge commits show forge
  account names. GitLab does not link them to GitLab users, and a name that
  looks like an email address is withheld, in declared approvers too. An
  email address someone writes into a change's body is published as written,
  but its `#`, `!` and `@` are separated from what follows and links lose
  their live scheme (`https:// …`), so nothing the forge copies to GitLab
  mentions anyone or closes an issue.
