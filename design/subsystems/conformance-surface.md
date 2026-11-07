# Conformance decisions and display

A verdict records an evaluation. A confirmation or exception records a person's
decision about that evidence. The current-status display combines those records;
it must not rewrite the original evaluation as if the evaluator had said something
else.

Conformance checks operation authority and admitted review scope. Confirmation
and exception commands retain the relevant project/check identity and their own
lifecycle rules. A matching description alone is insufficient identity: version
and source context matter.

Transports display owner projections and translate failures. A failed source,
policy or record read must not be presented as a successful evaluation. Fleet
reads apply project visibility internally; a narrowed query parameter is not an
authorization grant.

Project standing keeps finding counts and checks that could not run visible
together. Findings are amber; “N not known” is red in the standing chip and
summary note, including on phones. Findings retain priority in standing without
hiding incomplete evaluation. An attempted check that cannot give an answer is not known,
with its reason, and contributes no confirmed problem or judged check.
AI unclear and cannot-assess results remain on project Conformance with their
reason, Re-check and Record a decision. Recording a decision uses the existing
accepted-risk disposition for that check version: it does not turn uncertainty
into a pass. The decision remains attributed and revocable.

Routine deferrals stay not yet assessed and contribute no not-known count.
Conformance classifies each observation once when it is built, using the
exact verdict confirmation. Transports render that lane, including in live
review feeds. Required
checks that cannot give an answer still refuse the gate with their reason.

Use [conformance](conformance.md) for record ownership and [remediation](remediation.md)
for acting on findings.

## Current state and reviews

The project page and fleet roll-up lead with current state: the newest verdict
per adopted project/check from any review, including landings and remediation.
Each check shows its judged commit, date and numbered evaluation source. A result
from an older commit says “judged on an older commit”; missing evidence stays
not judged. A landing source names its numbered landing evaluation report.

Reviews are dated snapshot reports. Opening one shows only the evidence recorded
in that review, labelled as of its time, with a link back to current state.
Project review history follows the current checks and fixes. Current fixes are
not added to a historical snapshot.

## Implementation references

- [internal/apptown/conformance/fix.go](../../internal/apptown/conformance/fix.go)
- [internal/apptown/conformance/exception_commands.go](../../internal/apptown/conformance/exception_commands.go)
- [internal/apptown/conformance/service.go](../../internal/apptown/conformance/service.go)
- [internal/apptown/httpapi/fleet.go](../../internal/apptown/httpapi/fleet.go)
