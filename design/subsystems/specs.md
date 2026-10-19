# Product specifications

The stock `product-spec` workflow produces and reviews a specification artifact.
It uses normal agent, artifact and approval steps with the spec-development skill
and template. Those documents can guide later implementation work.

## No specification registry

There is no registry of requirement identities, diff-scoped compliance
judgments or recurring per-spec posture audits. The existing specification
artifact does not imply that every requirement has an executable check or a
persistent status record.

Before implementing the registry, define requirement identity/versioning, applicable
source, evidence, inability-to-assess outcomes and human decisions. Keep observed
behaviour separate from accepted product intent. A model's assertion alone is
not execution evidence.

Conformance and Remediation already own checks, verdicts and fix workflows. A spec
extension should compose those owners rather than create an independent approval
or workflow engine.

## Implementation references

- [stock/product-spec.yaml](../../stock/product-spec.yaml)
- [stock/guidance/skill/spec-development.md](../../stock/guidance/skill/spec-development.md)
- [internal/forge/conformance/service.go](../../internal/forge/conformance/service.go)
