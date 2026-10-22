CREATE TABLE IF NOT EXISTS adrs (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number        BIGINT NOT NULL,
  title         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'draft',
  body_md       TEXT NOT NULL DEFAULT '',
  tags          TEXT NOT NULL DEFAULT '',
  supersedes_id UUID,
  change_id UUID,
  created_at    TIMESTAMPTZ NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL,
  UNIQUE(project_id, number)
);
