-- What AppTown created for a preview, by the preview's name: today the
-- AI models key a preview of an app needing llm gets
-- (design/subsystems/integration-runtime.md). It goes when AppTown
-- keeps no record of the preview any more. No reference to the project: a
-- deleted project's preview keys are still revoked.
CREATE TABLE IF NOT EXISTS preview_owned (
  id UUID PRIMARY KEY,
  preview     TEXT NOT NULL,
  project_id UUID NOT NULL,
  kind        TEXT NOT NULL,
  ident       TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  recorded_at TIMESTAMPTZ NOT NULL,
  UNIQUE(preview, kind, ident)
);

-- When anyone last looked at a preview, by the preview's name: a request
-- to its address through AppTown, or a boot of it. A preview stops eight
-- hours after its last look; a change's preview keeps its build images and
-- snapshot for thirty days after it, and the look goes with them
-- (design/subsystems/container-lifecycle.md). Written on AppTown's
-- clock, never the database's.
CREATE TABLE IF NOT EXISTS preview_views (
  preview   TEXT PRIMARY KEY,
  viewed_at TIMESTAMPTZ NOT NULL
);
