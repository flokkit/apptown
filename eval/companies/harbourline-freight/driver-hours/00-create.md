---
from: Tomasz Wierzbicki, Fleet compliance manager
size: long
kind: create
exercises: creating a regulated-records web app with a nightly job, precise rules, worked examples and an amendment-only history
---

Driver Hours Log — what I need

At the moment drivers fill in paper work diaries and the depot clerks type them
into a spreadsheet once a week. By the time I see a fatigue breach it's eight
days old. When the regulator audited us in March it took two people three days
to pull the records together. I need drivers to log their own hours as they go,
and I need breaches flagged the next morning, not next week.

**Who uses it**

- Drivers (about 290) log their own work and rest on a phone or the depot
  tablet. They can see their own records only.
- Depot managers see the drivers based at their depot.
- I see everyone, and I'm the only one (with my deputy, Anika Shah) who can
  close a breach.

**What a driver logs**

Each entry is a period of **work** or **rest**, with a start time and an end
time, the truck rego (for work), and the start and end location (a depot or a
free-text place). Work includes driving and anything else for the company:
loading, paperwork, waiting at a customer. Driving is not logged separately.

Times are local time. Drivers log as they go or at the end of the shift. They
can log up to 3 days back; older than that has to go through their depot
manager.

**Company fatigue policy (solo drivers)**

These are Harbourline's own rules. They are deliberately a bit stricter than
the regulator's, so we never get close.

1. **Daily work.** No more than 12 hours of work in any 24-hour period.
2. **Daily rest.** At least 7 continuous hours of rest in any 24-hour period.
3. **Short breaks.** No more than 5 hours 15 minutes of work without a rest
   break of at least 15 continuous minutes.
4. **Weekly work.** No more than 72 hours of work in any 7 days (168 hours).
5. **Weekly rest.** At least 24 continuous hours of rest in any 7 days.
6. **Night work.** No more than 4 shifts in any 7 days that include work
   between 00:00 and 05:59.

"Any 24-hour period" means any window, not the calendar day: a window starts at
the end of every rest of 15 minutes or more. Any period with nothing logged
counts as **unknown**, not rest. An unknown gap inside a window is shown to the
depot manager to chase; it does not count towards rest.

**Worked examples**

- Sam works 06:00–11:00, rests 11:00–11:15, works 11:15–16:30. That's 5h00 then
  5h15 of work: fine on short breaks. Total 10h15: fine.
- Sam works 06:00–11:30 with no break → **breach of rule 3** at 11:15 (5h15
  reached with no 15-minute break). The breach is the moment it happened, not
  the end of the shift.
- Jo works 18:00–23:00, rests 23:00–23:15, works 23:15–04:00, rests 04:00–10:00
  (6 hours), works 10:00–13:00, rests 13:00–17:00, works 17:00–21:00. In the
  24 hours from 23:15, Jo worked 11h45 (fine) but the longest continuous rest
  was 6 hours → **breach of rule 2**. And the 23:15–04:00 shift counts as a
  night shift for rule 6.
- Lee's entries for Tuesday stop at 14:00 and start again Wednesday 05:00.
  That's an unknown gap, not 15 hours of rest. Lee's depot manager is asked to
  chase it. If Lee later logs Tuesday 14:00–Wednesday 05:00 as rest, the
  check reruns and the gap goes away.
- A shift that crosses midnight is one shift, and counts on both days for
  totals.

**Nightly job**

Every night at 02:00 check everyone's last 8 days against the six rules. Write
each breach with: driver, rule, the time it happened, the entries involved. The
first thing I see in the morning is a list of new breaches. Re-check when
someone logs or amends something older, too — don't wait for the nightly run
for that driver's own screen.

A breach stays **open** until I or Anika close it with a note (e.g. "spoke to
driver", "logging error, amended"). Closing never deletes it.

**Corrections**

Nothing is ever deleted. If a driver or manager fixes an entry, keep the
original, record who changed it, when and why (a reason is required). The
audit view shows an entry and every version of it. If an amendment makes a
breach disappear, the breach is not closed automatically: it shows "no longer
breaching after amendment" and still needs closing by me. Auditors look for
exactly that.

**Reports**

- For any driver and date range: every entry, every amendment, every breach,
  as a printable page and a CSV. This is what I hand the regulator.
- Monthly: breaches per depot per rule.

Two-up (team) driving has different rules; we only do a few team runs. Leave it
out for now, but don't let a team driver's hours break the solo checks —
mark a driver as "team" and skip them, with a note on the screen that they're
not checked yet.
