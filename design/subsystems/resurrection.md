# Reverse engineering and replacement evaluation

The stock `excavate` workflow and excavation skill support producing an evidence-
based requirements document from existing software. They use ordinary Workflow
execution, reports, review and human approval. There is no separate resurrection
engine in the daemon.

Observed behaviour, inferred intent and accepted requirements must remain distinct.
A source reference can establish what code says; a probe can establish a particular
observed result. Neither automatically establishes what a replacement should do.
Human adjudication resolves that product question.

The maintainers keep a parity harness, not in the published repository. It compares
specific replacement commands with committed reference outputs using documented
normalizations. It is an experiment, not a general proof that an arbitrary rewrite
preserves behaviour. For example, some generated images are checked for presence
rather than pixel equivalence.

Adapting it to a new target requires reviewing that target's corpus, comparison
rules and execution setup.

## Implementation references

- [stock/excavate.yaml](../../stock/excavate.yaml)
- [stock/guidance/skill/excavation.md](../../stock/guidance/skill/excavation.md)
