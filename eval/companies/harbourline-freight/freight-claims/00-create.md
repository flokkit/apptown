---
from: Mele Fifita, Claims officer
size: long
kind: create
exercises: creating a web app with object storage for photos, money thresholds tied to approval roles, and a kept record
---

Hello,

I'd like a proper system for freight claims. At the moment claims come in by
email, the photos sit in my inbox, I track them in a spreadsheet, and when
Graham asks why we paid someone $3,800 I have to dig through six email threads.
We get about 40 claims a month, more before Christmas.

**A claim**

- Customer (account name and number), the consignment number, the delivery
  date, the depot it went through, and who's claiming (name, email, phone).
- What happened: damaged, short (some items missing), lost (nothing arrived),
  or late (we don't pay for late, but people claim anyway).
- Amount claimed, in dollars, and the weight of the consignment in kg.
- Photos: of the damage, the packaging, the delivery docket. Usually 3–10
  photos, from phones. Also sometimes a PDF invoice.
- Notes, and every email I send or receive about it pasted or attached.

**Steps**

1. **Lodged.** I enter it (or the depot does). It gets a reference like
   HC-2026-0412.
2. **Assessing.** I check the photos, ask the depot for their side, and decide
   an amount to offer. The depot manager gets a request to add their report
   and any photos the driver took at delivery.
3. **Offer approved.** Who can approve the offer depends on the amount:
   - up to $500: I can approve it myself;
   - $500.01 to $5,000: the depot manager for the depot it went through;
   - over $5,000: Graham Achterberg (finance controller).
   The person approving sees the claim, the photos and the depot report on one
   page, and the amount they're approving. If the amount changes after
   approval, it needs approving again.
4. **Settled.** Paid (with the payment reference from finance), or declined
   (with a reason), or withdrawn by the customer.

**Our terms** (these are in our conditions of carriage)

- Claims should be lodged within 14 days of delivery. Late claims aren't
  rejected automatically — show them as **late lodgement** and I'll decide.
- Our liability is capped at $20 per kg of the consignment, or $2,000 per
  consignment, whichever is less, unless the customer bought extended cover
  (there's a yes/no on their account, I'll keep that in the claim for now).
  The app should work out the cap and show it next to the amount claimed. An
  offer above the cap needs a note explaining why (sometimes we pay over to
  keep a big customer).
- Late delivery: we don't pay. The app should still let me record it and
  decline it.

**Examples**

- 120 kg consignment, damaged, claimed $1,500, no extended cover. Cap is
  min(120 × $20, $2,000) = $2,000. Offer $1,500 → needs the depot manager.
- 40 kg, claimed $1,500, no cover. Cap is $800. Offer $800 → depot manager.
  Offer $1,000 → depot manager, and I must write why we're going over.
- 300 kg, extended cover, claimed $9,200. No cap. Offer $9,200 → Graham.
- Lodged 20 days after delivery → shown as late lodgement; I can still proceed.

**Rules about the record**

- Claims are never deleted. Wrong entries get corrected with a reason, and the
  old value is kept.
- Photos are never replaced; you can add more, or mark one "not relevant"
  but it stays.
- Every approval records who, when, the amount, and what they saw.

**Reports**

- Open claims by age and step, so nothing sits for weeks.
- Monthly paid out, by depot and by cause. Dana asks for this every month.

Photos can be big. They need to be stored somewhere that isn't the database,
and kept for 7 years like all our financial records.

Thanks
Mele
