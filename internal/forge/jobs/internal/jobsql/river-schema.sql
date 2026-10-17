-- Compile-time query surface of River v0.47.0's river_job table.
-- River's own migrations remain the sole runtime schema owner. This is not
-- assembled into Forge's schema or applied to the database. Jobs integration
-- tests execute every query against the schema installed by the pinned driver.
CREATE TYPE river_job_state AS ENUM ('available','cancelled','completed','discarded','pending','retryable','running','scheduled');

CREATE TABLE river_job (
 id bigint NOT NULL,
 args jsonb NOT NULL,
 kind text NOT NULL,
 state river_job_state NOT NULL,
 attempt smallint NOT NULL,
 errors jsonb[],
 metadata jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 attempted_at timestamptz,
 attempted_by text[] NOT NULL,
 finalized_at timestamptz,
 scheduled_at timestamptz NOT NULL
);
