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

Use [conformance](conformance.md) for record ownership and [remediation](remediation.md)
for acting on findings.

## Implementation references

- [internal/forge/conformance/fix.go](../../internal/forge/conformance/fix.go)
- [internal/forge/conformance/exception_commands.go](../../internal/forge/conformance/exception_commands.go)
- [internal/forge/conformance/service.go](../../internal/forge/conformance/service.go)
- [internal/forge/httpapi/fleet.go](../../internal/forge/httpapi/fleet.go)
