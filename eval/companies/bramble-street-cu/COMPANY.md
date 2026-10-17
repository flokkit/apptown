# Bramble Street Credit Union

A member-owned credit union, started in 1958 by workers at the old Bramble
Street tram depot. About 31,000 members, 140 staff and six branches: Bramble
Street (head office), Eastgate, Millbrook, Harrow Vale, Ashcombe and Port
Linden. It offers everyday accounts, savings and term deposits, personal and
car loans, and home loans. It is regulated as a deposit-taking institution and
answers to a prudential regulator, a consumer credit regulator and an external
complaints scheme. The board meets monthly; the Asset and Liability Committee
(ALCO) sets rates.

The software team is two developers and the IT lead. Most of their work used
to be spreadsheets and shared mailboxes; these apps replace them.

## People

| Name | Role | Asks for, approves |
|------|------|--------------------|
| Deborah Achterberg | Chief Executive Officer | Board reporting; final say on anything public that carries the credit union's name |
| Tomasz Wiśniewski | Member Services Manager | Owns Hardship Requests; runs the contact centre and hardship team |
| Leilani Kahale | Branch Manager, Eastgate (and acting area manager for the eastern branches) | Branch needs; a requester and first approver on cash orders |
| Arjun Ramaswamy | Treasury and Cash Officer | Owns Branch Cash Orders and the rate data; approves rate changes for treasury |
| Fiona MacLeod | Risk and Compliance Officer | Owns Complaints Register policy and all disclaimer text; the risk approver |
| Rosa Delgado-Marsh | Complaints and Dispute Resolution Officer | Day-to-day user of Complaints Register; reports to Fiona |
| Kwame Asante | Marketing Lead | Owns the information site and campaign sites |
| Hyun-woo Park | IT Lead | Runs the software team; owns deployments, backups and access |

## House rules

These are the rules the credit union wants its software and its software
changes to follow. Several came out of internal audit findings.

1. **Two approvals for money and member data.** Any change to an app that
   moves money, changes a limit, or touches members' personal details needs
   two approvals before it goes live, and one of them must be from risk
   (Fiona, or whoever acts for her). The person who asked for a change cannot
   be one of its approvers.
2. **No member details in logs or test data.** Member names, numbers, dates of
   birth, addresses, phone numbers, emails and account numbers never appear in
   application logs, error reports or test fixtures. Test data is invented.
3. **Public rates are approved twice.** A rate shown to the public (website,
   campaign sites) or to staff on the Rate Sheet is approved by treasury
   (Arjun) and by compliance (Fiona) before it is published, and it must match
   the ALCO decision it came from.
4. **Compliance owns disclaimer text.** Disclaimers, comparison-rate warnings,
   target market statements and the hardship and complaints wording on public
   pages are written or approved by Fiona. Marketing may place them but not
   edit them.
5. **One source for rates.** The Rate Sheet's rate data is the only source.
   The website and campaign sites read rates from it; nobody types a rate into
   a page.
6. **Regulated records are kept.** Hardship and complaint records, and cash
   order approvals, are kept for at least seven years and are never deleted
   by the apps. Corrections are made by adding a new entry, not by editing
   the old one.
7. **Business days are our business days.** Deadlines count business days on
   the credit union's calendar: Monday to Friday, excluding the public holidays
   on the list Fiona maintains.

## Apps

| Directory | App | Kind |
|-----------|-----|------|
| `hardship-requests/` | Hardship Requests | Internal web app |
| `cash-orders/` | Branch Cash Orders | Internal web app |
| `complaints-register/` | Complaints Register | Internal web app and a daily deadline job |
| `rate-sheet/` | Rate Sheet | Internal static site, published with sign-off |
| `website/` | Bramble Street CU information site | Public static site |
| `first-home/` | First Home Loan campaign site | Public site with an enquiry form, ends on a date |

Cross-app requests share a slug: `port-linden-closure` (cash-orders,
complaints-register, website) and `rate-change-november` (rate-sheet,
website, first-home).
