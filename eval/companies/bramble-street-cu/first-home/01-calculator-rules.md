---
from: Arjun Ramaswamy, Treasury and Cash Officer
size: long
kind: feature
exercises: a calculator with exact formulas, rounding and validation rules, worked examples and edge cases; must agree with the Rate Sheet's rate
---

Here's how the First Home calculator should work. Members will screenshot
it, so it has to be right.

## Inputs

- Property price: $100,000 to $2,000,000.
- Deposit: in dollars. At least 5% of the price.
- Loan term: 10 to 30 years, whole years, default 30.
- Repayment frequency: monthly, fortnightly, weekly. Default monthly.

Loan amount = price - deposit. If that's under $200,000, show "The First
Home Loan is for loans of $200,000 or more" and don't calculate.

## The rate

The calculator uses the current First Home Loan rate from the Rate Sheet
data, and shows it with its comparison rate exactly as the Rate Sheet does
("5.79% p.a., comparison rate 5.86% p.a.*"). If the rate data can't be
read, show "Rates are temporarily unavailable, please call us" rather
than a number. Never fall back to a rate written into the page.

## The formula

Standard principal and interest repayment:

    r = annual rate / 100 / periods per year
    n = years x periods per year
    repayment = loan x r / (1 - (1 + r)^-n)

Periods per year: monthly 12, fortnightly 26, weekly 52. (We don't do the
"half the monthly" fortnightly trick. It's not how our loans are set up.)

Round the repayment UP to the next cent. Show it with a dollar sign,
thousands separators and cents: $3,066.14.

Also show total interest over the term: repayment x n - loan, rounded to
the nearest dollar.

## LVR and LMI line

LVR = loan / price, shown as a whole percentage, rounded down.

- LVR 80% or under: "No lenders mortgage insurance needed."
- Over 80% up to 95%: "No lenders mortgage insurance for members who have
  saved with us for 12 months or more. Otherwise LMI applies."
- Over 95%: don't calculate. "Your deposit needs to be at least 5% of the
  price."

## Worked examples (at 5.79%)

1. Price $650,000, deposit $65,000, 30 years, monthly. Loan $585,000,
   LVR 90%. r = 0.0048250, n = 360. Repayment $3,428.79 (rounded up).
   Shows the 12-month member line.
2. Same, fortnightly. r = 0.0022269, n = 780. Repayment about $1,581 a
   fortnight; check the exact cents against the formula.
3. Price $250,000, deposit $60,000. Loan $190,000. Under $200,000, no
   calculation.
4. Price $500,000, deposit $20,000. Deposit is 4%. "Your deposit needs to
   be at least 5% of the price."

Please write tests that check these against the formula, not against
numbers copied from a website. If my rounded figures above are a cent out,
the formula wins; tell me.

## Small print under the calculator

Fiona's text, word for word: "This calculator is a guide only. It assumes
the interest rate does not change over the loan term and does not include
fees. It is not an offer of credit."
