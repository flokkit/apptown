---
name: lang/go/dependency-updates
tags: dependencies,go
fixes: [{"kind":"update","ecosystem":"Go","files":["go.mod","go.sum","go.work","go.work.sum"]}]
---

Run `go get -u ./...` and `go mod tidy` in every module. For workspaces, run `go work sync`. Major releases may require changing module paths and imports.
