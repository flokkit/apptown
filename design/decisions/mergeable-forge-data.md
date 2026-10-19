# Mergeable forge data

A value that only one database can interpret (an integer identity, a username,
a slug, a number counted per project) leaks into Git refs and trailers, receipts,
JSON payloads, file paths and generated text. Two forges' data would collide
everywhere: both have a change 293, a user `alice`, a `go-service` version 3.
So identity inside the forge never uses such values.

This is about the shape of the data, not a merge feature.

## Decision

**A record's identity is a UUIDv7, minted in Go when the record is created.**
A caller may mint it for a retry-safe creation. It is the only identity that
foreign keys, receipts, refs, trailers, APIs and stored files use. People see
display numbers and names, resolved at the edge ("egg-catch change 12",
"alice"); those are never used as identity inside the forge. Content is
identified by its digest. Authority never travels with identity.

A name the forge has already given to something outside itself (a branch, a
merge request, a backup object, a running container) is not renamed: the record
keeps the name it was given, and only what is created afterwards is named by UUID.

1. **Every durable record** has a UUIDv7 primary key, children included. A
   component identified completely by its parent and a stable component key
   (an attempt number, a step name) needs none. UUID order is mint order, not
   an event clock; where order means something, a record says so in a column.
2. **Display numbers are per scope and only display.** Project records are
   numbered within their project, as GitHub numbers issues; forge-wide records
   within the forge. A number may be reassigned when records are combined. Names
   (slugs, usernames, deployment names) are display too. Links to a record use
   its UUID; links to a project's own pages use its slug; a friendly number is
   computed from the current slug and is never stored as a reference.
3. **Actors are principals.** A person or the forge itself is a principal with
   a UUID. History stores the principal, the name as shown at the time, and how
   they acted (session, token, chat, run step, ticket, schedule). Decisions
   compare principals and read how the act was made; they never compare names.
   Agents are not principals: an agent always acts through someone's credential,
   chat or run.
4. **Content is identified by a digest of what it resolves to.** A library
   publication, transcript or blob is named by a hash of its resolved content,
   including the digests of what it includes, separate from the record that
   published it.
5. **Each forge has a UUID.** A record or principal from another forge keeps
   its origin forge's UUID and its original display number. A host name is
   where a forge is, not who it is.

A UUID identifies a record; it does not make it trusted or make an act human
assent. A digest identifies content; it is not its author's authority.

### Not promised

- A merge or import feature, its UI, its conflict resolution, or linking the
  same person across forges.
- Authority: grants, memberships, tokens, sessions, OAuth delegations, tickets
  and system bindings belong to the forge that issued them. A principal from
  another forge can never sign in or hold any of them. Receipts about authority
  are history, never live authority.
- Secret values; deployments, targets, previews and other running resources.
- Stable display numbers across a merge.
- Renaming anything outside the forge's own database and files, or rewriting
  Git history. Old commits, branches and objects keep their old numbers, read
  through the former-number record of their origin forge.
- Slug history: a renamed project's old page links break.

## Why

- **People.** History that names actors by free-text username breaks on a
  rename, lets a reused name inherit authorship and authority, and frees a
  name when its sign-in account is removed. A principal does none of those.
- **Moves, splits and consolidations** keep their records' identities instead
  of a renumbering map built by every owner.
- **Backups and exports** read elsewhere still name the records they are about.
- **Witness, do not assert.** "Change 293" is ambiguous once it leaves its
  forge; a UUID is not.

New tables follow the rule from the start; a schema test refuses an integer
identity.
