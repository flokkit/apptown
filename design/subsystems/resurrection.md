# Reverse engineering and replacement evaluation

Status: workflow and experimental tools; no separate subsystem. Checked on 18 September 2026.

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

The archived document retains the selected applications, historical results and
broader evaluation proposal. Those observations are not current deployment results.
Adapting a new target requires reviewing its corpus, comparison rules and execution
setup; this documentation pass did not run the experiments.

## Implementation references

- [stock/excavate.yaml](../../stock/excavate.yaml)
- [stock/guidance/skill/excavation.md](../../stock/guidance/skill/excavation.md)
