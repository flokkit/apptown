---
from: Tomasz Wiśniewski, Member Services Manager
size: long
kind: feature
exercises: a long rule-heavy request with thresholds, worked examples and edge cases; decisions that need a second approver from risk
---

Here are the assessment rules, agreed with Fiona last Thursday. Please build
these into the decision step. Assessors still use their judgment, but the app
should work out what it can and stop people recording a decision the policy
doesn't allow without the right sign-off.

## What the assessor enters

For the member (household, if it's a joint loan):

- monthly income after tax
- monthly essential expenses (rent or board, food, utilities, transport,
  childcare, other debt repayments)
- the reason category: job loss, reduced hours, illness or injury, family
  violence, separation, death of a partner, natural disaster, other
- whether the hardship is expected to be short term (under 6 months) or
  long term

For each loan in the request the app already knows the current monthly
repayment and remaining term (we'll type them in for now; see the end).

## Surplus and what we can offer

Surplus = income - essential expenses - total current repayments on our loans.

Repayment capacity = income - essential expenses (what's left for our loans).

| Situation | What the assessor may offer without extra approval |
|-----------|----------------------------------------------------|
| Surplus is zero or more | Nothing needed on affordability grounds. The assessor may still approve a pause of up to 1 month if the member has a short-term reason. |
| Capacity is at least 50% of current repayments | Reduced repayments down to the member's capacity, for up to 3 months |
| Capacity is below 50% but above zero | Reduced repayments for up to 3 months, or a full pause for up to 3 months |
| Capacity is zero or negative | A full pause for up to 3 months |

Anything beyond that table needs a second approval from risk (Fiona or her
delegate):

- any arrangement longer than 3 months in total (including extensions of an
  earlier arrangement on the same loan within the last 12 months)
- any term extension
- any reduction below the member's calculated capacity
- any request involving more than $150,000 of total loan balances

**Family violence** requests are never declined without risk's second approval,
and the assessor must not be required to collect income documents before
offering an initial pause of up to 3 months. The app must not show the
partner's details on these requests to anyone except the assessor and risk.

A **decline** always needs a written reason chosen from: member has capacity
to pay, member did not provide information after two requests, request is
for a loan we don't hold, other (with text). A decline also needs the second
approval if the member has an existing arrangement or has been in arrears in
the last 6 months.

## Deadlines

We must give the member a decision within 21 calendar days of receiving the
request, or within 21 days of getting the information we asked for if we
asked within the first 21 days. Show the due date on the list and colour
anything due in the next 3 days.

## Worked examples

1. Income $4,800, expenses $3,100, current repayment on one car loan $900.
   Surplus = 4,800 - 3,100 - 900 = $800. No affordability case. If the reason
   is illness and short term, a 1-month pause can be approved by the assessor.
2. Income $3,200, expenses $2,600, home loan repayment $1,100. Capacity
   = $600, which is 54.5% of $1,100. Assessor may approve reduced repayments
   of $600 a month for up to 3 months. Reducing to $400 needs risk.
3. Income $2,900, expenses $2,750, personal loan $480 and car loan $520.
   Capacity = $150, which is 15% of $1,000. Assessor may offer a pause or
   reduced repayments totalling $150 across both loans, up to 3 months.
4. Same member as example 2 comes back two months after a 3-month
   arrangement ended and asks for another 2 months. That's 5 months within
   12 months on the same loan, so it needs risk.
5. Joint home loan, balance $212,000, request for a 2-month pause after a
   job loss. Over $150,000, so risk approves even though the pause is short.

## Edge cases

- Income of zero is valid (job loss) and the app shouldn't error on it.
- If expenses aren't entered yet, don't calculate anything, show "not
  enough information" instead of a zero.
- Percentages show to one decimal place; dollar amounts to the cent.
- The second approver cannot be the assessor.
- If someone changes the income or expense figures after risk has approved,
  the approval no longer stands and has to be given again.

For now loan balances and repayments are typed in by the assessor. We'd like
to pull them from the core banking system later but that's not this request.
