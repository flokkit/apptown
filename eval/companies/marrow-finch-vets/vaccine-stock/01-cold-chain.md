---
from: Dr Adaeze Okonkwo-Hart, Clinical Director
size: long
kind: feature
exercises: a long rule-heavy feature with worked examples, edge cases and stock status changes driven by recorded readings
---

Following the problem at Inchbrook in August (the fridge door was left ajar over
a weekend and we only found out on the Monday), I want fridge temperature
readings recorded in Vaccine Stock, and I want stock that may have been
compromised to be held back automatically until a vet decides what to do.

## Readings

Each vaccine fridge gets readings twice a day, at opening and at closing. A
reading is: fridge, time taken, current temperature, the minimum and maximum
since the thermometer was last reset, who took it, and whether they reset the
thermometer afterwards. Temperatures are in °C to one decimal place.

The safe range is **2.0 to 8.0 °C inclusive**. A reading is out of range if the
current, minimum or maximum temperature is outside that range.

## What happens when something is out of range

1. The reading is saved as it was taken. Never refuse to save an out-of-range
   reading; that is exactly the one we need.
2. Every batch in that fridge goes on **hold**. Held stock is not counted as
   usable stock and cannot be recorded as used. It still counts towards what
   the clinic physically has.
3. The person entering the reading is asked whether the door was found open, the
   power was off, or something else (free text), and that is kept with the
   reading.
4. The clinic manager and the clinic's lead vet see the hold on their front page
   until it is resolved.
5. A vet resolves a hold per batch with one of: **release** (still usable, with
   a reason, usually "manufacturer says stable for X hours at Y °C"),
   **discard** (recorded as wasted, reason "cold chain"), or **return to
   supplier**. Nurses can enter readings but cannot resolve holds.

## Missing readings

If a fridge has no reading for a whole working day, show that on the clinic's
front page as "no readings yesterday" rather than as fine. A missing reading is
not the same as a good reading. Do not put stock on hold for a missing reading,
just flag it. Clinics that are closed on a day (Sundays at most clinics) are not
expected to have readings that day; Carrow Bridge and Northgate are open every day.

## Worked examples

- Ashcombe, Tuesday 08:10: current 5.1, min 3.9, max 6.8. All in range. Nothing
  happens.
- Inchbrook, Monday 08:05: current 11.4, min 4.0, max 14.2. Out of range. All
  batches in that fridge go on hold. Nurse notes "door found ajar".
- Brindle Street fridge 2, Thursday 18:00: current 4.0, min 1.6, max 6.0. Out of
  range because the minimum is below 2.0 (too cold is also bad; some vaccines
  must not freeze). Only fridge 2's batches go on hold, fridge 1 is unaffected.
- Exactly 8.0 is in range. 8.1 is out.
- A reading entered with min higher than max, or current outside min to max,
  should not save; it's a typing mistake. Ask them to check the thermometer.

## Edge cases

- A delivery arrives while a fridge has an open hold. The new stock is put in
  that fridge but is **not** on hold, because it was not there at the time.
- Stock moved from a held fridge to another clinic keeps its hold.
- If the same batch is released and then a later reading is out of range, it
  goes back on hold. Old decisions don't protect it.
- We need to be able to answer "what was the temperature history of the fridge
  this batch was in, between delivery and use?" if a client complains about a
  reaction. A simple page per batch is enough.
