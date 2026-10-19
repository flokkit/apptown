# The key use cases

These are the end-to-end journeys the forge
exists for. Each one crosses more than one role, and the points where one
role hands to another are where the product earns its keep: a decision is
made by the person answerable for it, and a receipt is written at the
boundary. `requirements.md` holds the atomic use cases by responsibility
(A running the forge, B owning and governing software, C contributing);
each step below cites the identifier it is built from, so nothing is
written twice.

Each key use case gives: who is involved, what starts it, the flow with
the responsible role at every step, the receipts written along the way,
and what has to be true at the end.

## K1. A change, from wish to shipped

**Roles:** maintainer · the helper · the forge.
**Starts when:** someone wants the software to be different.

1. **Maintainer** says what they want in the chat, or picks a lane and
   writes it down (C2, C4); a coding agent may do this for them (C20).
2. **Helper** opens the change on its own branch, works it, boots the
   preview, writes the report (C2, C6). *Receipt:* the run with each
   step's evidence and the steering it ran under.
3. **Reviewer** (an independent agent) checks the report against the
   diff; a misrepresentation fails it and the helper reworks (C rules).
4. **Maintainer** opens the preview, reads the report and the diff, and
   accepts or sends back with words (C7). *Receipt:* the acceptance, in
   their name.
5. **The forge** lands it: catch up with main, tests, scanners, the way
   of working's checks judged on what the change introduced (C15), the
   approval inherited from the acceptance, merge, image scan, deploy to
   main (C13). *Receipt:* the bundle and the release record.

**Done when:** `main` carries the merge, the standing deployment serves
it, and the change reads shipped. **The boundary that matters:** the
helper never accepts and the forge never decides; the one human judgment
is the maintainer's, and the landing runs under it.

## K2. Releasing to a wider audience

**Roles:** instance administrator · owner · release manager (or a
maintainer, for an unprotected deployment) · the forge.
**Starts when:** something on `main` should reach more people.

1. **Administrator** has added the deployment target (A6) and offered
   it to the project's group, with caps on audience and data class
   (A7).
2. **Owner** creates a standing deployment on that target with its
   audience and data class; the forge refuses what the offer or the
   contract cannot allow (B6).
3. **Release manager** deploys the latest bundle, or a maintainer
   releases to the intranet (B8, B9). *Receipt:* the release with its
   number, who, the commit and the image digest, and the deployment's
   log as it is written.
4. **The forge** fulfils the app's needs on the target, renders the
   contract, deploys as the next generation, and records it; after a
   restart it resumes or fails honestly (B rules).
5. **Release manager** rolls back by deploying an earlier bundle if it
   went wrong (B10).

**Done when:** the deployment serves the released bytes at its address.
**The boundary:** a protected deployment takes the release grant; the
owner can create the place but not promote to it without that authority.

## K3. Adopting a new way of working

**Roles:** standards administrator · owner · maintainer · the helper · the
forge.
**Starts when:** a pack has a newer version, or the owner wants a block
the project does not hold.

1. **Standards administrator** publishes: a stock release or an
   institution pack cuts new versions and moves no project (B16).
   *Receipt:* the version.
2. **Owner** sees the update available and prepares it: blocks brought,
   moved, dropped; lanes; the checks the app would newly be held to
   (B11, B13). A contradiction between blocks is refused here.
3. **Owner** adopts now if nothing in the code must change. *Receipt:*
   the new adoption revision. Or opens an uplift change that must pass
   the new checks.
4. **Helper** works the uplift change; **maintainer** accepts (K1 steps
   2–4).
5. **The forge** lands it under the target revision's checks, which must
   pass outright, and flips the project to the new revision when it
   lands (B13). *Receipt:* the revision, cited by every run after it.

**Done when:** the project's receipt names the new versions and the fleet
view shows it there. **The boundary:** a release never moves a project;
only the owner's adoption does, and weakening a gating check takes the
standards authority (B12).

## K4. Remediating the fleet

**Roles:** standards administrator · the helper · maintainer · the forge.
**Starts when:** a fleet review finds projects that no longer meet what
they adopted.

1. **Standards administrator** runs the review; every project's `main` is
   judged read-only and gets a standing and a worklist (B20).
2. **Standards administrator** opens fixes, one ordinary change per
   project, each carrying the check's own expectation as its acceptance
   criteria (B21, B23). *Receipt:* the remediation and its queue.
3. **The forge's worker pool** works them one project at a time; the
   queue can be paused, resumed, retried, skipped, and survives a
   restart (B23).
4. **Helper** works each change; **maintainer** accepts it (K1).
5. **The forge** lands each one; the check must pass outright, never
   excused as pre-existing (B21).

**Done when:** the projects' standings read conforms, or the remaining
findings are on the record (K5). **The boundary:** the standards side
decides what must be true; each project's own maintainer still accepts
the change that makes it true.

## K5. Accepting a finding on the record

**Roles:** requester (owner or maintainer) · a different approver ·
the forge.
**Starts when:** a finding will not be fixed, for now or ever.

1. **Requester** asks for an exception — accepted risk, deferred,
   compensating control — with a scope, a justification and an expiry
   (B22). For a scanner finding, a suppression with a reason and an
   author (C17).
2. **A different person** approves the exception; the forge refuses the
   requester approving their own (B22). *Receipt:* who asked, who
   approved, until when.
3. **The forge** treats the finding as excepted in reviews and gates
   until the expiry, then it counts again.

**Done when:** the finding shows as excepted with its record. **The
boundary:** two people by rule, and the clock.

## K6. Bringing a service into a project

**Roles:** instance administrator · owner · the helper · maintainer · the
forge.
**Starts when:** an app should report errors, take feedback, or use
something the institution runs.

1. **Administrator** connects the connector for the capability, tested
   before save, holding the names of secrets, never values (A2).
2. **Owner** attaches it to the project; the connection details reach the
   app's environments from then on; the code is unchanged (B26).
   *Receipt:* the project integration.
3. **Owner** wires it in: the change that makes the code read the
   connection, as the connector's steering prescribes (B27).
4. **Helper** works it; the reviewer checks it; **maintainer** accepts
   (K1).
5. **The forge** lands it; the card reads wired in once it ships.

**Done when:** the app reports through the service in a real deployment.
**The boundary:** the administrator provides, the owner decides to use,
the maintainer accepts the code. Swapping a provider is the same journey
with the new connector taking the capability's one slot (B28).

## K7. Retiring a service, and the connector behind it

**Roles:** owner · the helper · maintainer · instance administrator · the
forge.
**Starts when:** a service is being switched off.

1. **Owner** detaches it; contributions stop, nothing at the provider is
   deleted (B29). *Receipt:* the detach.
2. **The forge's retire workflow** removes the code that used it; an
   independent reviewer confirms nothing references it; **maintainer**
   accepts (B30, K1).
3. Repeat for every project that used the connector.
4. **Administrator** disconnects the connector; the forge refuses until
   the last project is clean (A3).

**Done when:** no project references the connector and it is gone from
the instance. **The boundary:** the administrator cannot pull a service
out from under a project; each project lets go first. This journey is
one project at a time today (`not-yet.md` N5).

## K8. Letting an agent act for you

**Roles:** a person · their coding agent · maintainer · the forge.
**Starts when:** someone wants their own agent to do the work.

1. **Person** connects the agent over MCP; it acts in their name within a
   narrower set of tools (C20). *Receipt:* the grant, listed and
   revocable (C21).
2. **Agent** reads the project and its guidance, opens a change, checks
   the branch out, pushes and syncs, runs the checks, boots the preview,
   and hands off for landing (C9, K1 steps 2–3).
3. **Maintainer** accepts (C7); the agent cannot.
4. **The forge** lands it (C13).

**Done when:** as K1. **The boundary:** the agent never approves, lands,
releases or administers, whatever the person could do themselves.

## K9. Standing your own rules in for the forge's

**Roles:** standards administrator · instance administrator · every
project · the forge.
**Starts when:** the institution wants its own landing, policy or check
on every project.

1. **Standards administrator** authors a workflow or clones a stock one,
   validated against the schema and the policies (B17); writes a policy
   (B18); or adds a check to a guidance block (B19). *Receipt:* the
   library version.
2. **Instance administrator** binds the custom system workflow in place
   of the stock one for every project at once (A13). *Receipt:* the
   binding.
3. **Every project's** next landing runs it; a landing that was waiting
   when the rules changed pauses and must be restarted (C13).

**Done when:** every landing on the instance runs the institution's
version and records its origin. **The boundary:** publishing is the
standards side; binding for everyone is the instance side; no project
can opt out of a system workflow, and every run says which one ran.

## K10. When the forge finds something wrong

**Roles:** the forge · instance administrator · owner.
**Starts when:** the forge's own checks find the record and the world
disagree.

1. **The forge** finds a project's repository dirty (only it writes
   there), a project whose way of working drifted from its receipt, or a
   landing or deployment it was in the middle of when it restarted (D).
2. **The forge** quarantines: the diff is snapshotted and shown, drift is
   never promoted, an interrupted landing or deployment is resumed from
   what was already named or failed honestly (D).
3. **Administrator** decides on the dirty repository, keep or throw away,
   and the decision is recorded (A14); **owner** resolves drift by
   adopting (B13).

**Done when:** the record and the world agree again, and the record says
who made them agree. **The boundary:** the forge notices and contains;
a person decides; nothing is absorbed silently.
