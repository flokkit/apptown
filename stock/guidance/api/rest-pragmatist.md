---
name: api/rest-pragmatist
tags: api,simplicity
---
APIs are REST-ish JSON under /api/v1, versioned by path, described by an
OpenAPI spec that generates the typed client. No HATEOAS ceremony.

Deviate by: gRPC when several services in several languages need typed RPC (ADR);
GraphQL approximately never for personal tooling.
