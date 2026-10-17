---
name: api/sse-over-websockets
tags: api,simplicity
---
Server push is Server-Sent Events. WebSockets only when traffic is genuinely
bidirectional.

Rationale: SSE is plain HTTP — proxies, auth, curl, and reconnection all just work.
