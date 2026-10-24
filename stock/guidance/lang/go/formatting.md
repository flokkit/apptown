---
name: lang/go/formatting
tags: lang,go
checks: [{"name": "gofmt-clean", "expects": "gofmt -l reports no file: every Go source in the tree is formatted the one way", "severity": "high", "evaluator": "tool", "tool": "gofmt", "title": "Format Go source files", "appliesTo": ["tool:go"]}, {"name": "go-vet-clean", "expects": "go vet ./... passes: no printf mismatches, unreachable code, copied locks, or the other things the compiler suspects but permits", "severity": "high", "evaluator": "tool", "tool": "go-vet", "title": "Resolve Go vet findings", "appliesTo": ["tool:go"]}]
---
Go has one formatter and it is not a matter of taste: every file is gofmt
output, and a diff shows only what changed. `go vet` is the other
mechanical check — it finds the mistakes the compiler permits (a printf
verb that does not match its argument, a lock copied by value,
unreachable code).

Run both before handing work off — `gofmt -l .` prints nothing and
`go vet ./...` says nothing — and never commit a formatting-only fix
alongside a change of behaviour; it drowns the review.

The checks below run the same two commands over the committed tree, so
what the gate holds a change to is exactly what `gofmt` and `go vet`
say.
