CREATE TABLE IF NOT EXISTS model_slots (
  slot       TEXT NOT NULL,
  project_id UUID,
  -- Instance rows (project_id NULL) carry a profile column name ('standard',
  -- 'high', ...). Project rows carry '' — an explicit per-project override
  -- that beats any profile (design/subsystems/models.md §6).
  profile    TEXT NOT NULL DEFAULT '',
  pattern    TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  -- One row per slot, project and profile; NULL (instance) counts as one value.
  UNIQUE NULLS NOT DISTINCT (slot, project_id, profile)
);
-- Where an unmapped slot degrades to (edits are deliberate acts; the chain
-- used is recorded in receipts via the winning slot).
CREATE TABLE IF NOT EXISTS slot_fallbacks (
  slot     TEXT PRIMARY KEY,
  fallback TEXT NOT NULL
);
