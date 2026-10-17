---
from: Arjun Ramaswamy, Treasury and Cash Officer
size: long
kind: feature
exercises: precise display and effective-date rules on the shared rate data, with worked examples; the rules other apps must follow too
---

A few months in, here are the rules for how rates are stored and shown.
These matter because the website and the campaign site will show the same
numbers and they must match to the digit.

## Storing rates

- Rates are stored as the exact figure ALCO approved, as a percentage with
  two decimal places, e.g. 6.24. Not as a fraction, not with more decimals.
- Each rate has an effective date. A rate change is a new entry with a later
  effective date, never an edit to the old entry. We need to show what the
  rate was on any past date (complaints ask this).
- Each change carries the ALCO meeting date it was approved at.

## Showing rates

- Always two decimal places followed by "% p.a.": 4.30% p.a., never 4.3%.
- Comparison rates appear next to the rate with an asterisk and the warning
  text Fiona gave us (in the footer). A loan rate without a comparison rate
  is not allowed on the page. If one is missing, the sheet shouldn't
  publish.
- Fixed-rate home loans show the revert rate (the variable rate they move
  to) underneath.
- Rates take effect at 12:01am on their effective date. Before then the
  current rate shows, with "Changing to X.XX% p.a. on [date]" underneath
  once the change is approved.
- A change approved with an effective date in the past is an error. Don't
  publish it, tell me.

## Term deposit tiers

If the $50,000+ tier has no separate rate, show the same rate in both
columns (don't leave it blank). If a term is withdrawn from sale, show
"Not available" and keep the row, so staff can answer existing members.

## Worked examples

1. On Monday 2 November, ALCO approves 3-month TD 4.30 (was 4.15) effective
   Monday 9 November. From approval until 12:01am on the 9th, the sheet
   shows 4.15% p.a. with "Changing to 4.30% p.a. on 9 November 2026". From
   the 9th it shows 4.30% p.a.
2. Variable owner-occupier 6.24, comparison 6.31. Shown as "6.24% p.a.,
   comparison rate 6.31% p.a.*".
3. 2-year fixed 5.89, comparison 6.18, reverts to variable owner-occupier
   6.24. Shown with "Reverts to 6.24% p.a. variable".
4. 24-month TD withdrawn from sale on 1 December. From then: "Not
   available". On 15 December someone asks what the rate was on
   20 November; we can look it up from the history.

## Not on the sheet

Staff-only notes (e.g. "match competitor up to 0.10 with manager approval")
must never appear on anything the public site reads. Keep them in a separate
field that the public data leaves out.
