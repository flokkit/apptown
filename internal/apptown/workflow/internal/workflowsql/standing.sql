-- name: InsertStandingRule :exec
INSERT INTO standing_rules(id,current_revision) VALUES($1,1);

-- name: StandingRuleHead :one
SELECT current_revision FROM standing_rules WHERE id=$1;

-- name: LockStandingRule :one
SELECT current_revision FROM standing_rules WHERE id=$1 FOR UPDATE;

-- name: AdvanceStandingRule :execrows
UPDATE standing_rules SET current_revision=sqlc.arg(next_revision) WHERE id=sqlc.arg(id) AND current_revision=sqlc.arg(expected_revision);

-- name: InsertStandingRevision :exec
INSERT INTO standing_rule_revisions(rule_id,revision,enabled,record_json) VALUES($1,$2,$3,$4);

-- name: StandingRevision :one
SELECT * FROM standing_rule_revisions WHERE rule_id=$1 AND revision=$2;

-- name: StandingHistory :many
SELECT * FROM standing_rule_revisions WHERE rule_id=$1 ORDER BY revision;

-- name: CurrentStandingRules :many
SELECT r.* FROM standing_rule_revisions r JOIN standing_rules h ON h.id=r.rule_id AND h.current_revision=r.revision ORDER BY r.rule_id;

-- name: StandingIndependentApproval :one
SELECT require_independent_approval FROM projects WHERE id=$1;
