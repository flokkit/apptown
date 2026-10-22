# Scanner findings and dispositions

The Check library normalizes supported scanner output into canonical findings.
Conformance adds baseline/provenance and human-disposition observations. Verification
runs tools, compares sources and applies gating policy. The original report remains
separate evidence from the normalized view.

A finding carries identifiers, severity, location/package context and a fingerprint
where available. Head/base comparison determines whether it is introduced,
pre-existing or undecided. Matching must preserve the source and rule context
needed by that comparison; a count alone cannot establish equivalence.

Dispositions are matched over findings for display and policy. Annotating a finding
with an exception does not change the scanner's original report. A fix is verified
through subsequent evidence, not by merely hiding an item in the current display.

A report is read with `conformance.ReadReport`, which returns an error for a
report it cannot parse in its scanner's format, never an empty list: views list
such a report as unread with the reason, and its findings as unknown, not zero.
See [quality](quality.md) for the complete evaluation and settlement sequence.

## Implementation references

- [internal/apptown/conformance/findings.go](../../internal/apptown/conformance/findings.go)
- [internal/apptown/verification/scan_provenance.go](../../internal/apptown/verification/scan_provenance.go)
- [internal/apptown/httpapi/findings.go](../../internal/apptown/httpapi/findings.go)
- [internal/apptown/conformance/findings_test.go](../../internal/apptown/conformance/findings_test.go)
