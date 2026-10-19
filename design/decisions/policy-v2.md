# Policy: rules anyone can follow

Every policy must be explainable to a first-time user who is not a developer.
Two questions always have visible answers: "what are our rules?" and "why
can't I release my software?" A policy that cannot say why it blocked someone,
in one plain sentence with what to do about it, is not finished.

## Three layers

1. **The sentence**: the author's description. "Nothing goes out without a
   security scan first."
2. **The precise reading**: rendered by the server from the rule and its
   options, so there is one source of truth and no drift in the interface. "Any
   workflow containing a `deploy` step must have a `scan` step in a strictly
   earlier stage; checked when workflows are saved."
3. **Standing**: who complies right now. Every adopting project, in line or
   not, with each violation naming the workflow, the reason and the fix ("add a
   scan step to a stage before the deploy step").

## Named rule types, never a predicate language

Rules are named types in the core with parameters
([formats guide](../../stock/docs/guides/ways-of-working-formats.md#policies)).
A single operator needs perhaps six rule forms. The rule vocabulary is shared
reality: growing it is a core change, made through the core's own gates. Packs
carry the adoption, with its origin; a rule blocks a workflow at save and is
checked again at each run.

## Policies and guidance are different things

Policies watch the rails: the system of work, its workflow shapes, bindings and
releases. Guidance watches the cargo: the code produced. Gates are per-run
enforcement of both. They cross-link; they are never merged.
