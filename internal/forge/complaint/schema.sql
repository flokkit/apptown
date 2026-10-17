CREATE TABLE IF NOT EXISTS complaints (
  id UUID PRIMARY KEY,
  -- How people name it ("complaint 9"): its number within the forge, from
  -- complaint_numbers, never reused.
  number BIGINT NOT NULL,
  at         TIMESTAMPTZ NOT NULL,
  project_id UUID,
  source     TEXT NOT NULL DEFAULT '',
  run_id UUID,
  workflow   TEXT NOT NULL DEFAULT '',
  step       TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL DEFAULT '',
  -- The filer's principal and way of acting (access.Actor); NULL when not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  text       TEXT NOT NULL,
  change_id UUID,
  why        TEXT NOT NULL DEFAULT '',
  wish       TEXT NOT NULL DEFAULT '',
  seen       BOOLEAN NOT NULL DEFAULT false,
  answer      TEXT NOT NULL DEFAULT '',
  answered_by TEXT NOT NULL DEFAULT '',
  -- The answerer's principal and way of acting (access.Actor); NULL when not resolved to a person.
  answered_by_principal UUID,
  answered_by_via TEXT NOT NULL DEFAULT '',
  answered_at TIMESTAMPTZ,
  archived_at TIMESTAMPTZ,
  -- What the complaint is about, chosen by the filer: forge (the platform),
  -- way-of-working (the project's adopted packs, named in pack_version by
  -- the forge) or project (the project's own code and guidance).
  about        TEXT NOT NULL DEFAULT 'forge',
  pack_version TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS complaints_number ON complaints(number);
-- The last number given to one of complaints: the next is one more, whatever was
-- deleted since, so a number never names a second record.
CREATE TABLE IF NOT EXISTS complaint_numbers (
  id   BOOLEAN PRIMARY KEY DEFAULT true CONSTRAINT complaint_numbers_one CHECK (id),
  last BIGINT NOT NULL
);
