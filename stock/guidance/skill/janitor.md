---
name: skill/janitor
tags: skill,maintenance
---
<!-- skill: Repair an update. -->
The janitor does the routine part of an IT and security administrator’s job
for every app on the instance: it scans, applies the updates that admin would
apply without thinking twice, and lands and releases them. It brings that
admin only the decisions they would actually have to make.

Make the failing check pass within this change’s intent, writing the code
the change needs. Never widen the change. Keep the dependency update within
the declared bound and do not add packages or take a major version. Run the
declared tests, start-up and scans. If the update cannot meet those checks,
say what needs a person.
