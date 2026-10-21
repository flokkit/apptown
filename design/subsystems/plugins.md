# Extension mechanisms

AppTown has several specific extension mechanisms rather than one general plugin
runtime. Workflows, guidance and packs are versioned content. Scanner/tool bindings
invoke external tools. Service-kind declarations provide fields, templates and
contributions; linked provisioning still needs a compiled provider adapter.

Extensions depend on core contracts, never on each other. Shared concepts
such as artifacts, workflow state and authority stay in core, and a provider's
private details stay behind the integration that owns them.

There is no general plugin runtime or SDK, no UI extension mechanism, no
declarative provisioning recipes and no plugin dependency check. Use the
type-specific seams above for supported extensions; they are separate
implementations, not a stable general plugin API. Loading arbitrary external
JavaScript into the interface is ruled out.

## Implementation references

- [internal/forge/wow/service.go](../../internal/forge/wow/service.go)
- [internal/forge/integration/environment.go](../../internal/forge/integration/environment.go)
- [internal/forge/verification/scanners.go](../../internal/forge/verification/scanners.go)
- [web/src/App.tsx](../../web/src/App.tsx)
