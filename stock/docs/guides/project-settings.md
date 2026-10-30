# Project settings

**Settings → General** edits the project's name, address, icon, group and
one-line description. **suggested** marks an AppTown-generated description;
**yours** marks one a person saved.

Saving a changed, nonempty description makes it yours. AppTown keeps it when
changes land, including when an earlier model call finishes after your edit.
Clearing it lets AppTown suggest a new description in the background. Without
a model, it stays empty. A failed model call keeps the current description. Repeating an unchanged nonempty description keeps its
current ownership.

API settings edits follow the same rules. They must send the
`expectedRevision` they read as `settingsRevision`. Generation advances that
revision when the text changes, so an older draft cannot overwrite it. If the
settings change while you edit, read the current values before saving again.

Eval copies keep their fixed description and do not generate suggestions.
