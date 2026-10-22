# Workflow policy rules

Ways of Working owns policy declarations and their validation. Adoption owns
the rules installed on a project and evaluates them against workflow definitions.
These rules are separate from Identity's authorization policy.

The implemented rule kinds are:

- `requireStepBefore`: require an ordering relationship between step types.
- `requireCapabilityBound`: require an instance integration capability binding.
- `scanFailOnCeiling`: constrain the severity threshold of scanning steps.

Capability-bound policy reads instance bindings. Adoption's separate required-
capability checks validate actual project attachments; the two checks are not
interchangeable. Evaluation prepares shared capability facts once in the caller's
database snapshot and propagates required-read failures.

Policy declarations use checked rule names and parameters. A policy is evidence
about the permitted workflow structure, not proof that a tool ran successfully or
that the current caller may approve work. Project selection/publication and
weakening authority remain with their respective application commands.

## Implementation references

- [internal/apptown/wow/policies.go](../../internal/apptown/wow/policies.go)
- [internal/apptown/adoption/policies.go](../../internal/apptown/adoption/policies.go)
- [internal/apptown/adoption/authority.go](../../internal/apptown/adoption/authority.go)
