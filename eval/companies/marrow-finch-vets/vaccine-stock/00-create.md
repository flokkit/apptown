---
from: Fenella Rourke, Group Practice Manager
size: medium
kind: create
exercises: creating a small internal web app with per-site data, dates and thresholds
---

We need a proper place to keep track of vaccine stock at each clinic. At the
moment every clinic has its own spreadsheet and half of them are out of date,
so we keep running out of things or throwing away expired vials.

What I'd like:

- A list of our 14 clinics, and for each one the vaccines it holds: product
  name, batch number, expiry date, how many doses, and which fridge it is in
  (most clinics have one vaccine fridge, Carrow Bridge and Brindle Street have two).
- Staff record stock coming in (delivery) and going out (used, wasted, sent to
  another clinic). The number on hand should follow from those, not be typed
  over.
- A reorder level per product per clinic. When a clinic drops to or below it,
  show it clearly on a page that clinic managers and I can look at.
- Anything expiring in the next 30 days should stand out, and anything already
  expired should be obvious and not counted as usable stock.
- A group-wide view for me: which clinics are low on what.

Clinic staff only need to see their own clinic. Clinic managers see their own
clinic. I need to see all of them. It's a web app; people will use it on the
treatment-room PC and sometimes on a tablet next to the fridge.
