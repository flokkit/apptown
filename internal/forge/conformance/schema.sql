-- Conformance (design/subsystems/conformance.md §2, §3.1): the read-only fleet posture
-- review. A review is an immutable, read-only record; each verdict is an
-- append-only record keyed to the exact commit, expectation, evaluator, and
-- mode it judged. "Current conformance" is a projection over the verdicts,
-- never a mutable cell. These tables hold evidence only — the review opens no
-- Change and touches no landing gate.
CREATE TABLE IF NOT EXISTS conformance_reviews (
  id UUID PRIMARY KEY,
  -- How people name it ("conformance review 9"): its number within AppTown, from
  -- conformance_review_numbers, never reused.
  number BIGINT NOT NULL,
  trigger     TEXT NOT NULL,            -- manual | scheduled | import | …
  actor       TEXT NOT NULL,
  -- The starter's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  started_at  TIMESTAMPTZ NOT NULL,
  finished_at TIMESTAMPTZ,
  projects    BIGINT NOT NULL DEFAULT 0,
  verdicts    BIGINT NOT NULL DEFAULT 0,
  errors      BIGINT NOT NULL DEFAULT 0,
  cost        DOUBLE PRECISION NOT NULL DEFAULT 0,
  status          TEXT NOT NULL DEFAULT '',   -- queued | running | done | error
  projects_total  BIGINT NOT NULL DEFAULT 0,
  projects_done   BIGINT NOT NULL DEFAULT 0,
  current_project TEXT NOT NULL DEFAULT '',
  -- What was asked, so the job that runs it reads it from here by ID:
  -- the selected project IDs (JSON null is the whole estate, [] none) and
  -- whether an exec-image tool may build, within build_budget (0 is the
  -- default budget).
  selected_projects JSONB NOT NULL DEFAULT 'null'::jsonb,
  build             BOOLEAN NOT NULL DEFAULT false,
  build_budget      BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS conformance_verdicts (
  id UUID PRIMARY KEY,
  review_id UUID NOT NULL REFERENCES conformance_reviews(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  project_slug  TEXT NOT NULL,
  sha           TEXT NOT NULL,           -- exact committed SHA evaluated
  block         TEXT NOT NULL,           -- the adopted guidance block
  version       BIGINT NOT NULL,        -- pinned expectation (block) version
  check_name    TEXT NOT NULL,
  evaluator     TEXT NOT NULL,           -- sh | llm | …
  evaluator_ver TEXT NOT NULL,           -- digest identifying the exact evaluator
  mode          TEXT NOT NULL,           -- posture | gate | runtime
  state         TEXT NOT NULL,           -- conforms | does-not-conform | error | cannot-assess | …
  severity      TEXT NOT NULL DEFAULT '',
  detail        TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL,
  excepted_by   TEXT NOT NULL DEFAULT '', -- exception reference when state=excepted; detail keeps the failure
  evidence_hash TEXT NOT NULL DEFAULT ''  -- AI verdicts: hash of the exact evidence bundle judged
);
-- A confirmation is an immutable adjudication of ONE verdict ("this AI flag is
-- real"), keyed to the verdict id — never a disposition, never applied to a
-- later evaluation of the same check (design/subsystems/conformance-surface.md §9).
CREATE TABLE IF NOT EXISTS conformance_adjudications (
  id UUID PRIMARY KEY,
  verdict_id UUID NOT NULL REFERENCES conformance_verdicts(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,             -- confirmed
  by         TEXT NOT NULL,
  -- The confirmer's principal and way of acting (access.Actor); NULL when not resolved to a person.
  by_principal UUID,
  by_via TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL
);
-- The exceptions in force when a review walked a project, copied at that
-- moment so the "as of review N" lens is a record, not a reconstruction.
CREATE TABLE IF NOT EXISTS conformance_review_exceptions (
  id UUID PRIMARY KEY,
  review_id UUID NOT NULL REFERENCES conformance_reviews(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  disposition_id UUID NOT NULL,
  fingerprint    TEXT NOT NULL DEFAULT '',
  identifier     TEXT NOT NULL DEFAULT '',
  package        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,
  justification  TEXT NOT NULL,
  by             TEXT NOT NULL,
  -- The disposition's decider's principal and way of acting, as the disposition recorded them.
  by_principal UUID,
  by_via TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL,
  expires_at     TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS conformance_fixes (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  check_key           TEXT NOT NULL,
  before_verdict_id UUID NOT NULL,
  after_verdict_id UUID NOT NULL,
  change_ids_json     JSONB NOT NULL DEFAULT '[]'::jsonb,
  commits             TEXT NOT NULL DEFAULT '',
  remediation_item_id UUID,
  accepted_by         TEXT NOT NULL DEFAULT '',
  -- The accepter's principal and way of acting (access.Actor); NULL when not resolved to a person.
  accepted_by_principal UUID,
  accepted_by_via TEXT NOT NULL DEFAULT '',
  created_at          TIMESTAMPTZ NOT NULL,
  UNIQUE(project_id, check_key, after_verdict_id)
);
CREATE TABLE IF NOT EXISTS dispositions (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  fingerprint   TEXT NOT NULL DEFAULT '',
  identifier    TEXT NOT NULL DEFAULT '',
  package       TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  justification TEXT NOT NULL,
  by            TEXT NOT NULL,
  -- The decider's principal and way of acting (access.Actor); NULL when not resolved to a person.
  by_principal UUID,
  by_via TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL,
  expires_at    TIMESTAMPTZ
);
-- A what-if (design/subsystems/conformance.md): a pack version's
-- shell and tool checks run on selected projects' main beside the checks
-- they adopted. Hypothetical by construction: no standing, gate, exception or
-- remediation query reads these tables. '' states are "not required".
CREATE TABLE IF NOT EXISTS what_if_reviews (
  id UUID PRIMARY KEY,
  pack           TEXT NOT NULL,
  pack_version   BIGINT NOT NULL,
  requested_by   TEXT NOT NULL,
  -- The requester's principal and way of acting (access.Actor); NULL when not resolved to a person.
  requested_by_principal UUID,
  requested_by_via TEXT NOT NULL DEFAULT '',
  state          TEXT NOT NULL DEFAULT 'queued',
  detail         TEXT NOT NULL DEFAULT '',
  projects_total BIGINT NOT NULL DEFAULT 0,
  projects_done  BIGINT NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL,
  finished_at    TIMESTAMPTZ,
  -- Model rubrics are judged only when the person who started it said so,
  -- with a spend limit; what they cost is kept, incomplete counting calls
  -- whose spend is unknown ("at least").
  rubrics            BOOLEAN NOT NULL DEFAULT false,
  spend_limit_micros BIGINT NOT NULL DEFAULT 0,
  cost_micros        BIGINT NOT NULL DEFAULT 0,
  incomplete         BIGINT NOT NULL DEFAULT 0,
  -- The selected project IDs (JSON null is the whole estate, [] none),
  -- read by the job that runs it.
  projects           JSONB NOT NULL DEFAULT 'null'::jsonb,
  -- advance_workflows: each project's selected workflows that no pack of
  -- the version pins move to the library's newest version (an explicit
  -- choice; otherwise they keep the version the project is on).
  advance_workflows  BOOLEAN NOT NULL DEFAULT false,
  -- queued: admitted, waiting for the conformance queue's worker.
  -- interrupted: AppTown stopped before it finished; it is not resumed.
  CONSTRAINT what_if_state CHECK (state IN ('queued','running','done','error','interrupted'))
);
CREATE INDEX IF NOT EXISTS what_if_reviews_version ON what_if_reviews(pack, pack_version, id);
CREATE TABLE IF NOT EXISTS what_if_rows (
  id UUID PRIMARY KEY,
  what_if_id UUID NOT NULL REFERENCES what_if_reviews(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  project_slug      TEXT NOT NULL,
  sha               TEXT NOT NULL DEFAULT '',
  adoption_revision BIGINT NOT NULL DEFAULT 0,
  candidate_digest  TEXT NOT NULL DEFAULT '',
  block             TEXT NOT NULL DEFAULT '',
  check_name        TEXT NOT NULL DEFAULT '',
  current_version   BIGINT NOT NULL DEFAULT 0,
  proposed_version  BIGINT NOT NULL DEFAULT 0,
  current_state     TEXT NOT NULL DEFAULT '',
  proposed_state    TEXT NOT NULL DEFAULT '',
  result            TEXT NOT NULL,
  detail            TEXT NOT NULL DEFAULT '',
  brief             TEXT NOT NULL DEFAULT '',
  history_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- reason: why a not-assessed row was not judged (conformance.NotAssessed).
  -- joins: the project is not on the pack and would join it.
  reason            TEXT NOT NULL DEFAULT '',
  joins             BOOLEAN NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS conformance_reviews_number ON conformance_reviews(number);
-- The last number given to one of conformance_reviews: the next is one more, whatever was
-- deleted since, so a number never names a second record.
CREATE TABLE IF NOT EXISTS conformance_review_numbers (
  id   BOOLEAN PRIMARY KEY DEFAULT true CONSTRAINT conformance_review_numbers_one CHECK (id),
  last BIGINT NOT NULL
);
