# Project notes

Project notes tell agents durable facts and preferences about one project.
They live in the repository's root `PROJECT-NOTES.md`, so proposing or changing
a note goes through a change and review. General rules belong in the project's
way of working.

Memory › Notes shows **What agents know about this project**, read from
the default branch. Each note names its author and source change. Acceptance
comes from that change's recorded approval decisions, not from the note's text.
If the source cannot be read, the card says so. Missing notes show **No notes yet**;
invalid or unreadable notes show an error.

**Write this project's notes** starts a change that reads the code and change
history and proposes starting notes. **Add a note** and **Remove** also start
changes. Review the proposed lines on the change Overview and accept screen.
Accepting the change accepts its notes; send it back to ask for corrections.
These actions do not write directly to the default branch.

The file uses this format:

```text
# Project notes
- Use real photos only. (Sam · change 4)
- Credit each photo in credits.json. (agent · change 5)
```

Use one attributed sentence per line, at most 40 notes and 6144 bytes. Attribute
a person's stated preference to that person and its source change; attribute
an agent's proposal to `agent`. Do not write an accepter into the file.
The stock platform pack's `repo/project-notes` block declares a repository
format check. A missing file passes. Existing projects receive this convention
only by explicitly adopting the new way-of-working version.

Adopted guidance declares the file as repository context. Workflow agents and
writable chats receive the file from their change's starting commit, before
any agent-selected file read. Project chats use the default branch's selected
commit. The retained initial briefing names the file and exact commit; oversized
context is truncated with a marker. A proposed note affects later changes after
it lands. The way of working and workflow take precedence over notes.

Notes are claims. AI-judged checks cannot use only `PROJECT-NOTES.md` or
`AGENTS.md` citations to establish application behaviour: those verdicts become
**cannot-assess**. A check declaring `evidenceUse: wording` can judge the document's
wording. Deterministic checks keep their normal interpretation. AppTown does not
edit the project's `AGENTS.md` or `CLAUDE.md` for the notes convention.
