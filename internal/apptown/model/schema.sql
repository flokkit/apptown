CREATE TABLE IF NOT EXISTS model_slots (
  slot       TEXT NOT NULL,
  profile    TEXT NOT NULL DEFAULT 'standard',
  pattern    TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (slot, profile)
);
-- Where an unmapped slot degrades to (edits are deliberate acts; the chain
-- used is recorded in receipts via the winning slot).
CREATE TABLE IF NOT EXISTS slot_fallbacks (
  slot     TEXT PRIMARY KEY,
  fallback TEXT NOT NULL
);
