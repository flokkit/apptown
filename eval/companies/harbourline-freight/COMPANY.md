# Harbourline Freight

Harbourline Freight is a road freight and warehousing company of about 430
people. Head office and the main cross-dock are at Port Kellan; there are four
other depots at Ridgeway, Tallow Creek, Merrin Junction and Southmoor. The fleet
is about 170 prime movers and 60 rigid trucks, run by roughly 290 drivers, most
of them on regular linehaul and metro runs between the depots and customer
warehouses. Harbourline has been family-owned for three generations; it grew by
buying smaller carriers, so its systems are a mix of spreadsheets, an old
transport management system (the TMS) and whatever each depot set up for itself.

The company has a small software team of its own and wants a handful of plain,
dependable internal tools to replace the spreadsheets, plus a public website
and a site for this year's driver recruitment drive.

## People who ask for things

| Name | Role | What they care about |
|---|---|---|
| Dana Okafor | General Manager, Operations | Runs the depots. Signs off spend. Wants one number she can take to the board. |
| Priya Raman | Dock operations lead, Port Kellan | The docks run on time. Her forklift drivers and gate staff use the tablets. |
| Hemi Walker | Depot manager, Tallow Creek | Speaks for the smaller depots, which are often forgotten. |
| Tomasz Wierzbicki | Fleet compliance manager | Driver hours, fatigue and licences. Answers to the regulator in an audit. |
| Anika Shah | Deputy fleet compliance manager | Acts for Tomasz when he's away; does the day-to-day breach follow-up. |
| Mele Fifita | Claims officer | Damaged and lost goods claims, and the customers who are cross about them. |
| Graham Achterberg | Finance controller | Fuel spend, claim payouts, month-end. Nothing pays out without a trail. |
| Siobhan Kearney | Marketing and communications manager | Everything the public sees under the Harbourline name. |
| Lucia Ferreira | People and recruitment lead | Hiring drivers; anything with pay, conditions or applicants' details. |
| Ravi Menon | IT and platform lead | Runs the servers, logins, backups and the house stack. |

## How we work (house rules)

These are Harbourline's own rules for changes to its software. They apply to
every app unless an app says otherwise.

1. **Driver records need compliance sign-off.** Any change that touches driver
   hours, fatigue calculations, licences or drivers' personal details must be
   approved by Tomasz Wierzbicki (or whoever is acting fleet compliance manager)
   before it goes live. Asking for the change is not approving it.
2. **Public pages need marketing approval.** Nothing goes onto the public
   website or the recruitment site without Siobhan Kearney's approval.
3. **Money rules need finance sign-off.** Changes to claim payout limits,
   approval thresholds, or fuel reconciliation tolerances need Graham
   Achterberg's approval.
4. **We keep the record.** Driver hours entries and claims are never deleted.
   A mistake is corrected by an amendment that keeps the original, records who
   changed it and why.
5. **House stack.** Internal web apps are server-rendered pages backed by
   Postgres; nightly and batch jobs run on the same stack. Public sites are
   static where they can be. Ask Ravi before bringing in a new language,
   database or hosted service.
6. **Works on the depot tablets.** Every internal page must be usable on the
   10-inch depot tablets in Chrome, outdoors, by someone wearing gloves: big
   targets, high contrast, no hover-only controls.
7. **No releases at shift start.** Nothing driver- or dock-facing is released
   between 04:00 and 07:00 local time, when the morning runs are leaving.
8. **Logins.** Staff use their Harbourline account. Until single sign-on is in
   place, each internal app has its own login managed by Ravi.

## Apps

| Directory | App | Kind |
|---|---|---|
| `dock-scheduler` | Dock Scheduler: book loading-dock slots, today's arrivals by bay | Web app and database |
| `driver-hours` | Driver Hours Log: drivers log work and rest, fatigue breaches flagged | Web app and nightly job |
| `freight-claims` | Freight Claims: lodge, assess and settle damaged-goods claims | Web app and object storage for photos |
| `fuel-reconciler` | Fuel Card Reconciler: match fuel-card statements to trips | Batch job and report page |
| `depot-directory` | Depot Directory: depots, hours, contacts | Static site (internal) |
| `website` | harbourline.example: the public company site | Public site with a contact form |
| `driver-recruitment` | Drive with Harbourline: this year's driver recruitment drive | Public campaign site with a form |
