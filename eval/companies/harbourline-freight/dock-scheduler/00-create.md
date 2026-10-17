---
from: Priya Raman, Dock operations lead
size: long
kind: create
exercises: creating a web app with a database from a long brief with booking rules, slot lengths and edge cases
---

Hi team,

We need to get dock bookings off the whiteboard and the shared spreadsheet. Right
now carriers ring the gate office, someone writes them on the board, and half
the time two trucks turn up for bay 7 at the same time and one of them sits in
the yard for an hour. Here is what I need. Start with Port Kellan; the other
depots will want it once it works.

**What's in it**

- Depots, each with numbered bays. Port Kellan has bays 1 to 12. Bays 1 to 4
  are refrigerated (they have the plug-ins and the cold curtain). Bays 11 and 12
  are side-loader only. Other depots have between 3 and 8 bays.
- Bookings: carrier name, truck rego, vehicle type, load type (inbound or
  outbound), reference number (the customer's PO or our consignment number),
  whether it's refrigerated, a contact phone, and the slot.
- Today's arrivals board: one screen per depot showing every bay down the side
  and today's bookings across, so the gate office and the forklift drivers can
  see what's coming. This goes on the big TV in the gate office and on the
  tablets.

**Slot lengths** (these are how long the truck is on the bay, including loading)

| Vehicle | Slot |
|---|---|
| Van or small rigid | 30 minutes |
| Rigid (8–12 pallets) | 45 minutes |
| Semi-trailer | 60 minutes |
| B-double | 90 minutes |

Refrigerated loads add 15 minutes, because of the temperature check and the
paperwork. So a refrigerated semi is 75 minutes.

**Rules**

1. A bay can only have one truck at a time. Back-to-back is fine: one booking
   ending at 10:00 and the next starting at 10:00 don't clash.
2. Slots start on the quarter hour (:00, :15, :30, :45).
3. Docks are open 05:00 to 22:00 Monday to Friday and 06:00 to 14:00 Saturday.
   Closed Sunday and public holidays. A booking must fit entirely inside opening
   hours.
4. Refrigerated loads only go on refrigerated bays. Non-refrigerated loads can
   go on a refrigerated bay, but only if no refrigerated bay is free for a
   refrigerated booking that is already waiting for that time (in practice:
   allow it, but show the person a warning, and let them go ahead).
5. B-doubles can't use bays 11 and 12 (they don't fit the side-loader apron).
6. Bookings can be made up to 14 days ahead. Not in the past.
7. When a truck arrives the gate marks it **arrived**; when it leaves, **departed**.
   If it hasn't arrived 15 minutes after the slot starts it shows as **late**
   (amber). At 30 minutes late it becomes **no-show** (red), and the bay is free
   again for walk-ins for the rest of that slot.
8. Walk-ins: the gate can put a truck that turned up without a booking into
   any free bay that fits the rules, starting now (rounded down to the quarter
   hour is fine).

**Worked examples** (Port Kellan, a Tuesday)

- Semi, not refrigerated, bay 7 at 09:00 → booked 09:00–10:00.
- Second semi asks for bay 7 at 09:30 → refused, clashes with the 09:00 booking.
  Offer the next free time on bay 7 (10:00) or another bay at 09:30.
- Refrigerated rigid asks for bay 5 → refused, bay 5 isn't refrigerated. Offer
  bays 1–4.
- B-double asks for bay 12 → refused, side-loader bay.
- Van at 21:45 → refused, a 30-minute slot would run past 22:00.
- Semi on Saturday at 13:00 → booked 13:00–14:00 (just fits).
- A semi booked 09:00 that hasn't arrived by 09:15 shows late; at 09:30 it's a
  no-show and a walk-in can have bay 7 until 10:00.

**Who uses it**

- Gate office staff (3 people per shift) make and change bookings, mark
  arrivals and departures.
- Forklift drivers just look at the board.
- I need to see a list of no-shows by carrier at the end of the week, because
  a couple of carriers are repeat offenders and I want to have a word.

Must work on the depot tablets with gloves on. Keep a history: if a booking is
moved or cancelled I want to see who did it and when.

Thanks,
Priya
