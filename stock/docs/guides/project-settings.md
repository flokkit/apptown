# Project settings

**Settings → General** edits the project's name, address, icon, group and
one-line description. **suggested** marks an AppTown-generated description;
**yours** marks one a person saved.

Saving a changed, nonempty description makes it yours. AppTown keeps it when
changes land, including when an earlier model call finishes after your edit.
Clearing it lets AppTown suggest a new description in the background. Without
a model, it stays empty. A failed model call keeps the current description. Repeating an unchanged nonempty description keeps its
current ownership.

If generation leaves the description empty, request another suggestion by
saving a short description, then clearing it and saving again. The form only
saves changed fields. Generation does not retry automatically.

API settings edits follow the same rules. They must send the
`expectedRevision` they read as `settingsRevision`. Generation advances that
revision when the text changes, so an older draft cannot overwrite it. If the
settings change while you edit, read the current values before saving again.
Sending `blurb: ""` explicitly requests generation even if it is already empty.
An active description job is kept; a finished or failed job allows a new one.

Eval copies keep their fixed description and do not generate suggestions.

Manage the project's [packs and adopted versions](packs-and-what-if.md) under
**Settings → Way of working**.

Project notes and decision records are under **Memory → Notes** and
**Memory → Decisions**.

**Settings → Models** shows the project's model profile and the model used for
each task. Choose a profile and select **Use profile** to change it. Changing
it requires the project's model-profile permission. The page also shows the
selected profile's task choices before you apply it.

Edit the profiles' task choices under **Integrations → Model profiles**.
Each profile lists its projects; another project's **… → Move project here**
opens that project's Models settings with the profile selected.
