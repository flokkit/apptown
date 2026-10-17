---
from: Ravi Menon, IT and platform lead
size: medium
kind: cross-app
exercises: a change starting in one app that must be repeated in Driver Hours Log, Freight Claims and the Fuel Card Reconciler report page
---

We want single sign-on with the Harbourline staff accounts, starting with the
Dock Scheduler and then Driver Hours Log, Freight Claims and the Fuel Card
Reconciler report page, in that order. People are sick of four passwords and
I'm sick of resetting them.

- Staff sign in with their Harbourline account. The app's own staff logins go
  away once everyone has signed in the new way at least once (I'll tell you when).
- Who can do what still lives in each app; SSO only says who someone is. Don't
  give anyone more access because they've got a Harbourline account.
- Carriers in the Dock Scheduler don't have Harbourline accounts and keep their
  own logins.
- When someone leaves and I disable their account, they must not be able to get
  into any of the apps, including a session they already had open. Within an
  hour is acceptable.

Do the Dock Scheduler first and show me before you start on the others.
Driver Hours Log will need Tomasz's sign-off since it's driver records.
