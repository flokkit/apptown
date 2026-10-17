---
name: data/postgres-maximalism
tags: data,storage,simplicity
---
When persistence must be networked, Postgres is the queue (SELECT … FOR
UPDATE SKIP LOCKED), the pub/sub (LISTEN/NOTIFY), the document store (jsonb), and the
search index (tsvector). New data infrastructure requires a project ADR containing the
measurement Postgres failed.

Rationale: one system to operate; second-best per capability is fine below serious scale.
