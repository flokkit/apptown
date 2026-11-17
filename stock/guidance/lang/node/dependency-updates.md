---
name: lang/node/dependency-updates
tags: dependencies,node
fixes: [{"kind":"update","ecosystem":"npm","files":["package.json","package-lock.json","npm-shrinkwrap.json","yarn.lock","pnpm-lock.yaml","bun.lock","bun.lockb"]}]
---
Verify updates with `npm ci` in a clean tree. If an optional platform package is missing from the lock, add it with `npm install --package-lock-only`.
