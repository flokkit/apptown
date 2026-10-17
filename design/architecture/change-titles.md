# Change title ownership

Work owns the Change title's words and editorial ownership. This is separate from
whether a Change is proposed, active, shipped or abandoned. `changeTitleOwner` has
three strict states, stored in one `title_ownership` column:

| Ownership | Meaning | Permitted automation |
| --- | --- | --- |
| human | A person or ordinary creation chose the title | None |
| awaiting_chat | A new chat explicitly requested first-message naming | One successful first-message claim |
| generated | The first chat message supplied a provisional title | A model may revise the words it observed |

One model decision and one checked private transaction participant own all title
changes. Pinning takes human ownership even when the chosen words are unchanged.
A blank human title remains ignored. Claiming the first message moves awaiting-chat
to generated; duplicates and later messages are harmless no-ops. A generated
revision is ignored if ownership or its observed words changed. There is no command
that unpins a human title. Initial creation permits only human or awaiting-chat.
Unknown/zero stored states are errors, not guessed defaults.

Human edit and narrative commands delegate title changes inside their existing
project-first transactions. Their required receipts still commit with all selected
fields and notes. The first-message command likewise commits its title and audit
together. The participant reads a fresh scoped row, applies the decision, writes
with the observed ownership/words guard and checks the affected row count.

New chat creation sends `titleFromChat: true`; the intent is persisted with the
Change before workspace effects. Workflow/backlog creation cannot request that
intent. Other creation paths use human ownership. Title wording has no operational
meaning: a person can deliberately choose `Chat change (…)` without handing over
ownership, and an awaiting chat can have any placeholder text.

The console attempts naming only after delivery succeeds. Naming failure is logged
and does not ask the person to resend an already delivered message or interrupt a
long-lived session. A failed first-message claim leaves the title awaiting a future
explicit message; there is no recovery/replay mechanism. The first sentence retains
the existing punctuation convention and 64-byte bound, now preserving UTF-8.

Schema 73 renames and converts the old boolean in one checked migration. True maps
to generated; false maps to human. Every other field, including historical title
text, remains unchanged. Old placeholder-like text with false ownership remains
human-owned because its original intent is ambiguous. No prefix-based migration,
archive rewrite, dual column or runtime compatibility reader is retained. Null
source values and unexpected dependencies stop migration and roll back. Stop the
old daemon before a future rollout; it cannot run against the new column.

This completes the title ownership model inside Work. It does not finish the
larger Work extraction or move authorization out of Forge's composition layer.
The shared title participant adds a fresh title read and, when needed, a separate
checked title update within human/narrative transactions; this slice removes
competing owners, not total SQL round trips.
