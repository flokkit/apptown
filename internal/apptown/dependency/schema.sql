CREATE TABLE IF NOT EXISTS dependency_kinds (
  capability TEXT PRIMARY KEY,
  spec_yaml  TEXT NOT NULL,
  origin     TEXT NOT NULL DEFAULT 'stock',
  updated_at TIMESTAMPTZ NOT NULL
);
