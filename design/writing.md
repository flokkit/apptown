# How AppTown writes

For everything a person reads: screens, command-line messages, errors, guides,
the README and the site.

## The test

Read it aloud. If a person wouldn't say it to a colleague, rewrite it.

## Rules

1. **Say what it means for the reader, not how it works inside.** "Anyone can
   say they are anyone" beats a description of which header is trusted.
2. **People do things. Software doesn't believe, hold, carry, witness or
   settle.** Name who acts, or use the plain verb: "AppTown checks", "you
   approve", "the change is blocked".
3. **Use everyday words.** Our product terms are fine once defined (change,
   run, landing, deployment, need, pack, way of working; see the glossary).
   Our internal words are not: admit, settle, mint, receipt, owner, witness,
   surface, attachment, fed by, instance.
4. **Errors say what went wrong and what to do next,** in that order. Only
   say "wait" or "try again" when that will help.
5. **One idea per sentence.** Cut chains of semicolons and dashes. Cut
   "in short", "note that", "simply", "just".
6. **No clever lines.** No metaphors, no sayings, no rhythm for its own sake.
   Say what is true, not what isn't: don't set up a claim nobody made just to
   deny it ("It isn't X. It's Y."). Avoid groups of three by habit and a short
   punchline at the end of a paragraph.
7. **Never make it sound safer or surer than it is.** A rewrite keeps every
   failure, approval, limit and "we don't know" that the original had.
   Unknown stays different from failed, and failed from not run.

## Before and after

All "before" lines are real.

**Security note**
- Before: In the default `open` authentication mode the browser names the
  person it acts for, and AppTown believes it.
- After: In `open` mode there is no sign-in: anyone who can reach AppTown can
  say they are anyone.

**Error when releasing to main**
- Before: a deployment fed by every landing is deployed by no person; start a
  promotion-fed deployment from a copy
- After: main deploys every change that lands, so it can't be released by
  hand. To choose when an app goes out, make a separate deployment and
  release to that.

**Permissions page**
- Before: Nothing is implied: a power nobody was granted, nobody holds.
- After: Nobody gets a permission by default. If no one was given it, no one
  has it.

**Token error**
- Before: the token has no purpose; an operator must record whether it is a
  cli or an agent token
- After: This token isn't marked as a command-line or an agent token. An
  administrator needs to set which one it is.

**Registry error**
- Before: the registry attachment could not be read: …; nothing new was
  minted
- After: Couldn't read this project's registry settings, so no new registry
  login was made: …

**Approval rules**
- Before: Instance administrators are exempt from author and approver-list
  restrictions. The required number of votes still applies.
- After: Administrators can approve a change even if they wrote it or aren't
  on the approver list. They still count as one vote.

**Backups on a target**
- Before: No copy runner chosen: backups and restore tests of deployments on
  this target are unavailable.
- After: Backups are off for this target. Choose where backup jobs run to
  turn them on.

**Who approves**
- Before: They don't approve other people's changes. Each project's own
  people do that.
- After: Each project sets its own approval rules.

**Chat with no model key**
- Before: the helper's screen never accepted the message — wait a few seconds
  and try again
- After: No AI model is set up yet. An administrator adds an API key on the
  Home page.

## What not to touch

- Text the agents read (pack guidance and prompts). Changing it changes how
  they work, so it goes through evals, not a rewrite.
- Exact names: commands, flags, file names, field names and error codes that
  scripts may match on.
