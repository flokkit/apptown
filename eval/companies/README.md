# Companies

Five made-up organisations, each with a handful of ordinary internal and public
applications, and the requests their people would send to build and then shape
those applications over time. The set has two uses:

- **Populating a demo forge** with a portfolio that looks like a real
  institution's: several teams, several kinds of app, a history of changes.
- **A test set.** Each request is something a person would actually type. Run
  in order, a company's requests exercise project creation, small and large
  changes, regulated records, scheduled jobs, public pages, and changes that
  must stay consistent across sibling apps.

The companies are invented. Any resemblance to a real organisation is an
accident; none of them should be presented as real.

## Layout

```
eval/companies/
  README.md                      this file
  <company>/
    COMPANY.md                   who they are, the people who ask for things,
                                 and the company's own way of working
    <app>/
      00-create.md               the request that creates the app
      01-<slug>.md               later requests, applied in order
      02-<slug>.md
      ...
```

Each company has three to five internal apps and at least one public site
(the company's own information site, and where it fits a project or campaign
site).

## A request file

Each file is one request, written as the person would write it: in their words,
with their level of detail, not as a specification Forge would prefer. A short
header says who sent it and what it is for; the request itself follows the
rule.

```
---
from: Priya Raman, Dock operations lead
size: short | medium | long
kind: create | feature | fix | content | rule | cross-app | data | ops
exercises: one line on what this request tests in Forge (for the test-set use)
---

The request text, exactly as it would be sent.
```

- `size`: short is a sentence or two; medium a paragraph or a short list;
  long is a page with rules, examples and edge cases.
- `kind`:
  - `create`: a new app;
  - `feature` and `fix`: ordinary changes;
  - `content`: text and pages;
  - `rule`: the company's way of working changes, for example a new review
    requirement;
  - `cross-app`: a change that must land consistently in several apps;
  - `data`: imports and corrections;
  - `ops`: schedules, backups, deployments.
- Requests are applied in number order. A later request may depend on an
  earlier one, never the reverse.
- `from` names a person from `COMPANY.md`, so requests carry real authority
  differences: someone who may ask for a thing, and someone who must approve it.
- `after: <app>/<NN>` (optional, may repeat) names a request in another app of
  the same company that must be finished first.

## Requests that span apps

A `cross-app` request is filed under the same slug in each app it changes,
for example `southmoor-closure.md`. The copies may sit at different numbers in
different apps, because each app's history differs; match them by slug, not
number. Some copies hold the full request and the others point to it. A run
that applies them should treat each slug as one change landing in several
projects, on the same day where the request says so.

Order holds within an app. Across apps, a few requests depend on another app's
earlier request. For example, Coldwater's accessibility rule covers the public
pothole map, which `pothole-reports/06` creates. Apply all of a company's
`00-create` requests first. After that, apply a cross-app or `rule` request
only once every request it names has been applied. Where a request depends on
another app's request, its header says so with `after:`.

## What the set contains

| Company | Apps | Requests |
| --- | --- | --- |
| Harbourline Freight (logistics) | dock-scheduler, driver-hours, freight-claims, fuel-reconciler, depot-directory, website, driver-recruitment | 50 |
| Marrow & Finch Veterinary Group (14 clinics) | vaccine-stock, referral-tracker, drugs-register, roster-swap, website, dental-month | 47 |
| Coldwater Shire Council (local government) | pothole-reports, dog-registration, hall-bookings, planning-notices, meeting-papers, website, main-street-upgrade | 52 |
| Bramble Street Credit Union (member-owned lender) | hardship-requests, cash-orders, complaints-register, rate-sheet, website, first-home | 45 |
| Tidewell Grammar School (K–12) | excursion-consents, library-loans, relief-board, uniform-shop, sports-results, website, open-day | 56 |

Some requests are written to be refused or questioned rather than carried out:
a request from someone without the authority to approve it; a public photo
that the school's consent rule forbids; a vague ask that needs a question
back. A good result for those is the refusal or the question, not the change.

## Running it against a forge

`run.py` sends the set through a forge using only the product: the `forge`
CLI with `--json`, plus the pack picker, a target's capacity and a project's
deployments, read from the API the web uses.

```sh
export FORGE_EVAL_STATE=~/forge-eval-state   # or --state FILE on each command
python3 eval/companies/run.py --forge https://forge.example --token-file ~/.forge-token \
    create --wave 1 --target built-in-docker      # or --company NAME
python3 eval/companies/run.py --forge … drive --target built-in-docker
python3 eval/companies/run.py --forge … next      # one more request per app, in order
python3 eval/companies/run.py --forge … report
```

- `create` makes each app's project with the pack `stacks.yaml` names and
  opens its `00-create` request. `stacks.yaml` sets the stack of every app by
  hand: spread across every supported server pack so a long-running test
  operates all of them, matched to what each app does, and grouped into
  `waves:`, one company per wave, so a fixed host's memory fills in steps
  (`create --wave N`). An app with no entry is not created. The forge's own
  pack picker is still asked, and its suggestion is recorded beside the chosen
  pack in the state and the report: it is measured, not used.
- `drive` follows every open change with `forge wait`: it approves the
  questions the forge asks, as the credential it is given, with a comment
  naming whom the runner acts for (`--on-behalf-of`, by default the user the
  token acts as); it lands a change whose
  work is done; it records how each ends. A failed gate stays failed.
- `next` opens one more request per app, following the order rules above, so
  a long-running forge gets a steady trickle of work.
- Capacity: before each new app and each first landing it reads the target's
  capacity and stops when less than 15% of the host's memory would stay free,
  counting 256 MiB (an estimate of use) for every app it has opened that is
  not running yet. Unknown use is not free memory.
- State is kept outside the repository, in the file `--state` names or in a
  file per forge under the directory `FORGE_EVAL_STATE` names; with neither the
  runner refuses. It is written before each mutation. If
  the forge's answer to a mutation is lost, that request is not sent again
  until `reconcile` records what a person found.
