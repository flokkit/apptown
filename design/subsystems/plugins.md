# Extension mechanisms

Status: specific extension mechanisms implemented; general framework proposed. Checked on 18 September 2026.

Forge has several specific extension mechanisms rather than one general plugin
runtime. Workflows, guidance and packs are versioned content. Scanner/tool bindings
invoke external tools. Service-kind declarations provide fields, templates and
contributions; linked provisioning still needs a compiled provider adapter.

The accepted dependency rule is that extensions depend on core contracts, not
on each other. Shared concepts such as artifacts, workflow state and authority
remain in core. Private provider details should stay behind the integration that
owns them. These are design constraints, not proof that a universal plugin SDK
already exists.

## UI extension

Declarative contributions rendered by core-owned components are the proposed UI
extension model. The slot vocabulary for tabs, cards, detail rows and forms is
not yet defined. Loading arbitrary external JavaScript bundles is outside the
accepted design.

The Codex chat milestone depends on that vocabulary. It does not require a complete
plugin marketplace or every proposed backend extension first.

## Remaining work

Declarative provisioning recipes, a plugin dependency check and a sample-plugin
conformance suite remain proposed. The current type-specific seams should be
used for supported extensions; do not infer a stable general plugin API from
those separate implementations.

## Implementation references

- [internal/forge/wow/service.go](../../internal/forge/wow/service.go)
- [internal/forge/integration/environment.go](../../internal/forge/integration/environment.go)
- [internal/forge/verification/scanners.go](../../internal/forge/verification/scanners.go)
- [web/src/App.tsx](../../web/src/App.tsx)
