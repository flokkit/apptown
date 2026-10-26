-- Access: who the accounts and groups are, what they hold, and the
-- credentials that prove who is asking. Record-store style
-- (design/architecture/record-store-style.md): whole rows by identity,
-- locks named as such, writes named for the operation, reports last.

-- ===== users ==========================================================

-- name: UserByName :one
SELECT * FROM users WHERE username = $1;

-- ===== principals =====================================================

-- InsertPersonPrincipal makes a person principal for a name no live
-- principal holds; nothing comes back when one does (LivePersonByName).
-- name: InsertPersonPrincipal :one
INSERT INTO principals (id, kind, name, created_at) VALUES (sqlc.arg(id), 'person', sqlc.arg(name), sqlc.arg(at))
ON CONFLICT (name) WHERE removed_at IS NULL AND origin_forge IS NULL DO NOTHING
RETURNING id;

-- name: LivePersonByName :one
SELECT id FROM principals WHERE name = $1 AND kind = 'person' AND removed_at IS NULL AND origin_forge IS NULL;

-- name: PrincipalByID :one
SELECT * FROM principals WHERE id = $1;

-- RemovePrincipal tombstones a person: the row stays for history, the
-- name is free.
-- name: RemovePrincipal :execrows
UPDATE principals SET removed_at = sqlc.arg(at)::timestamptz WHERE id = sqlc.arg(id) AND removed_at IS NULL;

-- name: UsersAll :many
SELECT * FROM users ORDER BY username;

-- Grants expire by AppTown's clock (at), as ActiveGrants judges them.
-- name: AdminUsers :many
SELECT u.* FROM users u WHERE EXISTS (
 SELECT 1 FROM grants g WHERE g.role_name='instance-administrator' AND g.capability='' AND g.scope_type='instance'
 AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>sqlc.arg(at)::timestamptz)
 AND ((g.subject_type='user' AND g.subject=u.principal_id::text) OR (g.subject_type='group' AND
 (g.subject='all-authenticated-users' OR EXISTS(SELECT 1 FROM group_members m WHERE m.group_name=g.subject AND m.principal_id=u.principal_id AND m.revoked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>sqlc.arg(at)::timestamptz)))))
) ORDER BY u.created_at, u.username;

-- name: NeedsBootstrap :one
SELECT NOT EXISTS(SELECT 1 FROM instance_settings WHERE key='setup_completed');

-- name: LockUsers :exec
LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE;

-- TouchUser records a name seen on the wire: a new name becomes a member
-- with no password, a known one gets its last-seen stamp.
-- name: TouchUser :one
INSERT INTO users (username, principal_id, created_at, last_seen) VALUES (sqlc.arg(username), sqlc.arg(principal_id), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (username) DO UPDATE SET last_seen = excluded.last_seen
RETURNING *;

-- RecordUser records a person a grant names, when there is no account by
-- that name yet; it is not a sighting, so last_seen stays empty.
-- name: RecordUser :exec
INSERT INTO users (username, principal_id, created_at) VALUES ($1, $2, $3) ON CONFLICT (username) DO NOTHING;

-- name: SeedAdministrator :one
INSERT INTO users (username, principal_id, created_at, last_seen) VALUES (sqlc.arg(username), sqlc.arg(principal_id), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (username) DO UPDATE SET last_seen = users.last_seen RETURNING *;

-- name: BootstrapAdministrator :one
INSERT INTO users (username, principal_id, password_hash, created_at, last_seen) VALUES (sqlc.arg(username), sqlc.arg(principal_id), sqlc.arg(password_hash), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (username) DO UPDATE SET password_hash = excluded.password_hash
RETURNING *;

-- name: UserByNameForUpdate :one
SELECT * FROM users WHERE username = $1 FOR UPDATE;

-- SaveUser persists the whole account after its object rules have run.
-- name: SaveUser :one
INSERT INTO users (username, principal_id, password_hash, created_at, last_seen, disabled_at, default_group)
VALUES (sqlc.arg(username), sqlc.arg(principal_id), sqlc.arg(password_hash), sqlc.arg(created_at), sqlc.narg(last_seen), sqlc.narg(disabled_at), sqlc.arg(default_group))
ON CONFLICT (username) DO UPDATE SET password_hash = excluded.password_hash,
  created_at = excluded.created_at, last_seen = excluded.last_seen, disabled_at = excluded.disabled_at, default_group = excluded.default_group
RETURNING *;

-- UsersSignedIn is every name that has ever signed in: it holds a session,
-- an agent connection, or a personal token that has been used.
-- name: UsersSignedIn :many
SELECT username FROM sessions_named
UNION SELECT username FROM delegations_named
UNION SELECT username FROM auth_tokens_named WHERE last_used IS NOT NULL;

-- name: DeleteUser :execrows
DELETE FROM users WHERE username = $1;

-- ===== sessions =======================================================

-- name: InsertSession :one
INSERT INTO sessions (id, token_hash, principal_id, created_at, expires_at) VALUES ($5, $1, (SELECT u.principal_id FROM users u WHERE u.username = $2), $3, $4) RETURNING id;

-- name: SessionByHash :one
SELECT sessions_named.* FROM sessions_named WHERE sessions_named.token_hash = $1 AND NOT EXISTS (
 SELECT 1 FROM external_signin_sessions x WHERE x.session_id=sessions_named.id AND NOT EXISTS (
 SELECT 1 FROM external_signin_providers p JOIN external_signin_bindings b ON b.provider_id=p.id
 WHERE p.id=x.provider_id AND p.enabled AND p.current_slot AND p.revision=x.provider_revision
 AND b.id=x.binding_id AND b.revoked_at IS NULL AND b.principal_id=sessions_named.principal_id
 ));

-- name: SessionByID :one
SELECT sessions_named.* FROM sessions_named WHERE sessions_named.id = $1 AND NOT EXISTS (
 SELECT 1 FROM external_signin_sessions x WHERE x.session_id=sessions_named.id AND NOT EXISTS (
 SELECT 1 FROM external_signin_providers p JOIN external_signin_bindings b ON b.provider_id=p.id
 WHERE p.id=x.provider_id AND p.enabled AND p.current_slot AND p.revision=x.provider_revision
 AND b.id=x.binding_id AND b.revoked_at IS NULL AND b.principal_id=sessions_named.principal_id
 ));

-- name: TouchSession :exec
UPDATE sessions SET last_used = sqlc.arg(at)::timestamptz WHERE token_hash = sqlc.arg(token_hash);

-- name: DeleteSession :execrows
DELETE FROM sessions WHERE token_hash = $1;

-- DeleteUserSessions ends every session of an account and reports how
-- many there were against how many went, so a removal that did nothing
-- is visible to the caller.
-- name: DeleteUserSessions :one
WITH selected AS MATERIALIZED (
  SELECT s.token_hash FROM sessions_named s WHERE s.username = $1
), removed AS (
  DELETE FROM sessions USING selected WHERE sessions.token_hash = selected.token_hash RETURNING sessions.token_hash
)
SELECT (SELECT count(*) FROM selected)::bigint AS selected, (SELECT count(*) FROM removed)::bigint AS removed;

-- ===== personal access tokens =========================================

-- name: InsertAuthToken :one
INSERT INTO auth_tokens (name, principal_id, role, token_hash, purpose, created_at) VALUES ($1, (SELECT u.principal_id FROM users u WHERE u.username = $2), $3, $4, $5, $6) RETURNING name;

-- name: AuthTokenByHash :one
SELECT * FROM auth_tokens_named WHERE token_hash = $1;

-- name: AuthTokensAll :many
SELECT * FROM auth_tokens_named ORDER BY created_at DESC;

-- name: TouchAuthToken :exec
UPDATE auth_tokens SET last_used = sqlc.arg(at)::timestamptz WHERE token_hash = sqlc.arg(token_hash);

-- name: AuthTokenByName :one
SELECT * FROM auth_tokens_named WHERE name = $1;

-- name: AuthTokenByNameForUpdate :one
SELECT t.*, u.username FROM auth_tokens t JOIN users u ON u.principal_id = t.principal_id WHERE t.name = $1 FOR UPDATE OF t;

-- LiveAuthTokenNamesOfUser is what a name's removal revokes.
-- name: LiveAuthTokenNamesOfUser :many
SELECT name FROM auth_tokens_named WHERE username = $1 AND revoked_at IS NULL ORDER BY name;

-- name: RevokeAuthToken :one
UPDATE auth_tokens SET revoked_at = sqlc.arg(revoked_at)
WHERE name = sqlc.arg(name) AND revoked_at IS NULL RETURNING name;

-- ===== groups and memberships =========================================

-- name: GroupByName :one
SELECT * FROM groups WHERE name = $1;

-- name: GroupByNameForShare :one
SELECT * FROM groups WHERE name = $1 FOR SHARE;

-- GroupsLive is every unretired group: built-in first, then personal, then by name.
-- name: GroupsLive :many
SELECT * FROM groups WHERE retired_at IS NULL ORDER BY kind = 'built-in' DESC, kind = 'personal' DESC, name;

-- name: MembershipsAll :many
SELECT * FROM group_members_named ORDER BY group_name, username;

-- LockPersonalGroup serialises the minting of one person's own group.
-- name: LockPersonalGroup :exec
SELECT pg_advisory_xact_lock(hashtextextended('personal-group:' || sqlc.arg(username)::text, 0));

-- name: InsertPersonalGroup :one
INSERT INTO groups (name, description, kind, created_by, created_by_principal, created_by_via, created_at) VALUES (sqlc.arg(name), sqlc.arg(description), 'personal', sqlc.arg(created_by), sqlc.arg(created_by_principal), sqlc.arg(created_by_via), sqlc.arg(created_at)) RETURNING *;

-- EnsureBuiltinGroup creates the built-in group once; a retired or
-- differently-kinded row of that name is not touched and returns nothing.
-- name: EnsureBuiltinGroup :one
INSERT INTO groups (name, description, kind, created_by, created_at) VALUES ($1, $2, 'built-in', 'apptown-system', $3)
ON CONFLICT (name) DO UPDATE SET name = excluded.name WHERE groups.kind = 'built-in' AND groups.retired_at IS NULL
RETURNING *;

-- name: SaveGroup :one
INSERT INTO groups (name, description, created_at) VALUES ($1, $2, $3)
ON CONFLICT (name) DO UPDATE SET description = excluded.description
RETURNING *;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE name = $1;

-- InsertMembership is strict: the row comes back, so a write that did
-- nothing is an error (personal group minting).
-- name: InsertMembership :one
INSERT INTO group_members (group_name, principal_id, created_by, created_by_principal, created_by_via, created_at) VALUES (sqlc.arg(group_name), (SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(username)), sqlc.arg(created_by), sqlc.arg(created_by_principal), sqlc.arg(created_by_via), sqlc.arg(created_at)) RETURNING group_name;

-- DeleteGroupMemberships requires the complete old member list to be removed.
-- name: DeleteGroupMemberships :one
WITH selected AS MATERIALIZED (
  SELECT m.group_name, m.principal_id FROM group_members m WHERE m.group_name = $1
), removed AS (
  DELETE FROM group_members USING selected
  WHERE group_members.group_name = selected.group_name AND group_members.principal_id = selected.principal_id
  RETURNING group_members.principal_id
)
SELECT (SELECT count(*) FROM selected)::bigint AS selected, (SELECT count(*) FROM removed)::bigint AS removed;

-- name: DeleteUserMemberships :execrows
DELETE FROM group_members WHERE principal_id = (SELECT u.principal_id FROM users u WHERE u.username = $1);

-- ===== grants =========================================================

-- name: GrantByID :one
SELECT * FROM grants_named WHERE id = $1;

-- name: LockGrant :exec
SELECT 1 FROM grants WHERE id = $1 FOR UPDATE;

-- InsertGrant records a grant unless an identical one is live; the
-- partial unique index grants_live_identity decides, so two writers
-- cannot both create one. No row back means one was already live.
-- name: InsertGrant :one
INSERT INTO grants (id, subject_type, subject, role_name, capability, scope_type, scope_id, origin, granted_by, granted_by_principal, granted_by_via, reason, created_at)
VALUES (sqlc.arg(id), sqlc.arg(subject_type), CASE WHEN sqlc.arg(subject_type)::text = 'user' THEN COALESCE((SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(subject)::text)::text, '') ELSE sqlc.arg(subject)::text END, sqlc.arg(role_name), sqlc.arg(capability), sqlc.arg(scope_type), sqlc.arg(scope_id), sqlc.arg(origin), sqlc.arg(granted_by), sqlc.arg(granted_by_principal), sqlc.arg(granted_by_via), sqlc.arg(reason), sqlc.arg(created_at))
ON CONFLICT (subject_type, subject, role_name, capability, scope_type, scope_id) WHERE revoked_at IS NULL DO NOTHING
RETURNING *;

-- name: LiveGrantByIdentity :one
SELECT * FROM grants_named
WHERE subject_type = sqlc.arg(subject_type) AND subject = sqlc.arg(subject) AND role_name = sqlc.arg(role_name)
  AND capability = sqlc.arg(capability) AND scope_type = sqlc.arg(scope_type) AND scope_id = sqlc.arg(scope_id) AND revoked_at IS NULL;

-- GrantEverRecorded: whether this role was ever granted to the subject at
-- the scope, live or revoked; a revoked one records an explicit choice.
-- name: GrantEverRecorded :one
SELECT EXISTS(SELECT 1 FROM grants_named
  WHERE subject_type = sqlc.arg(subject_type) AND subject = sqlc.arg(subject) AND role_name = sqlc.arg(role_name)
    AND capability = '' AND scope_type = sqlc.arg(scope_type) AND scope_id = sqlc.arg(scope_id));

-- name: LiveRoleGrantsForSubject :many
SELECT * FROM grants_named
WHERE subject_type = sqlc.arg(subject_type) AND subject = sqlc.arg(subject) AND role_name = sqlc.arg(role_name)
  AND scope_type = sqlc.arg(scope_type) AND scope_id = sqlc.arg(scope_id) AND revoked_at IS NULL
ORDER BY id;

-- name: ActiveGrants :many
SELECT g.* FROM grants_named g
WHERE g.revoked_at IS NULL
  AND (g.expires_at IS NULL OR g.expires_at > sqlc.arg(at)::timestamptz)
  AND (
    (g.subject_type = sqlc.arg(subject_type)::text AND g.subject = sqlc.arg(subject)::text)
    OR (sqlc.arg(subject_type)::text = 'user' AND g.subject_type = 'group' AND (
      g.subject = sqlc.arg(builtin_group)::text
      OR EXISTS (
        SELECT 1 FROM group_members m
        WHERE m.group_name = g.subject AND m.principal_id = (SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(subject)::text)
          AND m.revoked_at IS NULL
          AND (m.expires_at IS NULL OR m.expires_at > sqlc.arg(at)::timestamptz)
      )
    ))
  )
ORDER BY g.id;

-- ActiveGrantsOfPrincipal is ActiveGrants for a person by principal: their
-- own grants, their groups' and everyone's.
-- name: ActiveGrantsOfPrincipal :many
SELECT g.* FROM grants_named g
WHERE g.revoked_at IS NULL
  AND (g.expires_at IS NULL OR g.expires_at > sqlc.arg(at)::timestamptz)
  AND (
    (g.subject_type = 'user' AND g.id IN (SELECT r.id FROM grants r WHERE r.subject_type = 'user' AND r.subject = sqlc.arg(principal)::uuid::text))
    OR (g.subject_type = 'group' AND (
      g.subject = sqlc.arg(builtin_group)::text
      OR EXISTS (
        SELECT 1 FROM group_members m
        WHERE m.group_name = g.subject AND m.principal_id = sqlc.arg(principal)::uuid
          AND m.revoked_at IS NULL
          AND (m.expires_at IS NULL OR m.expires_at > sqlc.arg(at)::timestamptz)
      )
    ))
  )
ORDER BY g.id;

-- name: GrantsAtScope :many
SELECT * FROM grants_named
WHERE scope_type = $1 AND scope_id = $2 AND revoked_at IS NULL
ORDER BY subject_type, subject, id;

-- name: AllGrants :many
SELECT * FROM grants_named WHERE revoked_at IS NULL
ORDER BY scope_type, scope_id, subject_type, subject, id;

-- RevokeGrant stamps the grant's revocation; its subject is not
-- rewritten (it holds a principal's id for a person).
-- name: RevokeGrant :one
UPDATE grants SET subject_type = sqlc.arg(subject_type),
role_name = sqlc.arg(role_name), capability = sqlc.arg(capability),
scope_type = sqlc.arg(scope_type), scope_id = sqlc.arg(scope_id),
origin = sqlc.arg(origin), granted_by = sqlc.arg(granted_by), reason = sqlc.arg(reason), created_at = sqlc.arg(created_at),
expires_at = sqlc.narg(expires_at), review_at = sqlc.narg(review_at), revoked_at = sqlc.narg(revoked_at)
WHERE id=sqlc.arg(id) AND revoked_at IS NULL RETURNING id;

-- name: SetGrantExpiry :one
UPDATE grants SET expires_at = CAST(sqlc.arg(expires_at)::text AS timestamptz) WHERE id=sqlc.arg(id) RETURNING id;

-- LiveGrantIDsOfUser is every live grant naming a person directly, for
-- that name's removal.
-- name: LiveGrantIDsOfUser :many
SELECT id FROM grants_named WHERE subject_type = 'user' AND subject = $1 AND revoked_at IS NULL ORDER BY id;

-- name: SeedTransparentReads :exec
INSERT INTO grants (id, subject_type, subject, role_name, capability,
                    scope_type, scope_id, origin, granted_by, reason, created_at)
SELECT sqlc.arg(id)::uuid, 'group', sqlc.arg(subject)::text, sqlc.arg(role_name)::text,
       '', 'instance', '', 'profile:transparent',
       'apptown-system', 'transparent structure: everyone signed in can read everything', sqlc.arg(at)::timestamptz
WHERE NOT EXISTS (
  SELECT 1 FROM grants
  WHERE subject_type = 'group' AND subject = sqlc.arg(subject)::text
    AND role_name = sqlc.arg(role_name)::text AND capability = ''
    AND scope_type = 'instance' AND scope_id = ''
);

-- ===== instance settings ==============================================

-- name: LockInstanceSetting :exec
SELECT pg_advisory_xact_lock(hashtextextended('access-setting:' || sqlc.arg(key)::text, 0));

-- name: InstanceSetting :one
SELECT value FROM instance_settings WHERE key = $1;

-- name: InsertInstanceSettingOnce :exec
INSERT INTO instance_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING;

-- name: PutInstanceSetting :one
INSERT INTO instance_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value
RETURNING *;

-- name: InsertDecision :one
INSERT INTO authorization_decisions (id, decided_at, outcome, reason_code, principal_type, principal,
  delegation_kind, delegation_id, capability, resource_type, resource_id, project_id, grant_ids_json, roles_json,
  membership_paths_json, constraints_json, context_json, reason)
VALUES (sqlc.arg(id), sqlc.arg(decided_at), sqlc.arg(outcome), sqlc.arg(reason_code), sqlc.arg(principal_type), sqlc.arg(principal),
  sqlc.arg(delegation_kind), sqlc.arg(delegation_id), sqlc.arg(capability), sqlc.arg(resource_type), sqlc.arg(resource_id), sqlc.arg(project_id),
  sqlc.arg(grant_ids_json), sqlc.arg(roles_json), sqlc.arg(membership_paths_json), '{}', sqlc.arg(context_json), sqlc.arg(reason))
RETURNING *;

-- ===== oauth clients, codes, consents =================================

-- name: InsertClient :one
INSERT INTO oauth_clients (client_id, name, redirect_uris, created_at) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ClientByID :one
SELECT * FROM oauth_clients WHERE client_id = $1;

-- name: TouchClient :exec
UPDATE oauth_clients SET last_used = sqlc.arg(at)::timestamptz WHERE client_id = sqlc.arg(client_id);

-- name: InsertCode :one
INSERT INTO oauth_codes (code_hash, client_id, principal_id, redirect_uri, scopes, resource, code_challenge, auth_time, created_at, expires_at)
VALUES ($1, $2, (SELECT u.principal_id FROM users u WHERE u.username = $3), $4, $5, $6, $7, $8, $8, $9) RETURNING code_hash;

-- name: CodeByHash :one
SELECT * FROM oauth_codes_named WHERE code_hash = $1;

-- ConsumeCode is the one-use claim: the affected count says who won.
-- name: ConsumeCode :execrows
UPDATE oauth_codes SET used_at = sqlc.arg(at)::timestamptz WHERE code_hash = sqlc.arg(code_hash) AND used_at IS NULL;

-- A consent request expires by AppTown's clock, which writes its expiry
-- and judges it (ConsumeConsent, DeleteExpiredConsents).
-- name: InsertConsent :exec
INSERT INTO oauth_consents (token_hash, principal_id, browser_hash, request_json, expires_at)
VALUES (sqlc.arg(token_hash), (SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(username)), sqlc.arg(browser_hash), sqlc.arg(request_json), sqlc.arg(expires_at));

-- ConsumeConsent claims a consent request once, only for the same person,
-- browser and exact request, while it lives.
-- name: ConsumeConsent :one
DELETE FROM oauth_consents
WHERE token_hash = sqlc.arg(token_hash) AND principal_id = (SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(username)) AND browser_hash = sqlc.arg(browser_hash)
  AND request_json = sqlc.arg(request_json)::jsonb AND expires_at > sqlc.arg(at)::timestamptz
RETURNING token_hash;

-- ===== delegations and their tokens ===================================

-- name: InsertDelegation :one
INSERT INTO delegations (id, principal_id, client_id, client_name, audience, kind, scopes_json, auth_time, auth_method, created_at, expires_at)
VALUES ($10, (SELECT u.principal_id FROM users u WHERE u.username = $1), $2, $3, $4, 'oauth', $5, $6, $7, $8, $9) RETURNING id;

-- name: DelegationByID :one
SELECT * FROM delegations_named WHERE id = $1;

-- name: DelegationByIDForUpdate :one
SELECT d.*, u.username FROM delegations d JOIN users u ON u.principal_id = d.principal_id WHERE d.id = $1 FOR UPDATE OF d;

-- name: DelegationsLiveForUser :many
SELECT * FROM delegations_named WHERE revoked_at IS NULL AND username = $1 ORDER BY id DESC;

-- name: DelegationsLive :many
SELECT * FROM delegations_named WHERE revoked_at IS NULL ORDER BY id DESC;

-- name: TouchDelegation :exec
UPDATE delegations SET last_used = sqlc.arg(at)::timestamptz WHERE id=sqlc.arg(id);

-- A delegation expires by AppTown's clock (at), which wrote its expiry
-- and judges it live (Delegation.Live).
-- name: RevokeExpiredDelegations :execrows
UPDATE delegations SET revoked_at = sqlc.arg(at)::timestamptz WHERE revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at < sqlc.arg(at)::timestamptz;

-- name: InsertTokenPair :execrows
INSERT INTO oauth_tokens (token_hash, kind, delegation_id, created_at, expires_at)
VALUES (sqlc.arg(access_hash), 'access', sqlc.arg(delegation_id), sqlc.arg(at), sqlc.arg(access_expires)),
       (sqlc.arg(refresh_hash), 'refresh', sqlc.arg(delegation_id), sqlc.arg(at), sqlc.arg(refresh_expires));

-- name: TokenByHash :one
SELECT * FROM oauth_tokens WHERE token_hash = $1;

-- name: TokenByHashAndKind :one
SELECT * FROM oauth_tokens WHERE token_hash = $1 AND kind = $2;

-- name: RefreshTokenForUpdate :one
SELECT * FROM oauth_tokens WHERE token_hash = $1 AND kind = 'refresh' FOR UPDATE;

-- name: RevokeRefreshToken :execrows
UPDATE oauth_tokens SET kind = sqlc.arg(kind), delegation_id = sqlc.arg(delegation_id),
created_at = sqlc.arg(created_at), expires_at = sqlc.arg(expires_at), revoked_at = sqlc.narg(revoked_at)
WHERE token_hash = sqlc.arg(token_hash) AND kind = 'refresh' AND revoked_at IS NULL;

-- A token's revocation time is AppTown's clock, as for every other
-- revocation of it: DeleteDeadTokens keeps it a day by that clock.
-- name: RevokeDelegationTokens :one
WITH selected AS MATERIALIZED (
  SELECT t.token_hash FROM oauth_tokens t WHERE t.delegation_id=sqlc.arg(delegation_id) AND t.revoked_at IS NULL
), removed AS (
  UPDATE oauth_tokens SET revoked_at = sqlc.arg(at)::timestamptz FROM selected
  WHERE oauth_tokens.token_hash = selected.token_hash RETURNING oauth_tokens.token_hash
)
SELECT (SELECT count(*) FROM selected)::bigint AS selected, (SELECT count(*) FROM removed)::bigint AS removed;

-- RevokeTokensMintedSince ends what a replayed code minted.
-- name: RevokeTokensMintedSince :execrows
UPDATE oauth_tokens SET revoked_at = sqlc.arg(at)::timestamptz
WHERE delegation_id IN (SELECT d.id FROM delegations_named d WHERE d.client_id = sqlc.arg(client_id) AND d.username = sqlc.arg(username) AND d.created_at >= sqlc.arg(since))
  AND revoked_at IS NULL;

-- ===== git tickets (object capabilities) ==============================

-- name: InsertTicket :one
INSERT INTO object_capabilities (id, kind, principal_id, delegation_id, resource_type, resource_id, constraints_json, token_hash, created_at, expires_at)
VALUES ($10, $1, (SELECT u.principal_id FROM users u WHERE u.username = $2), $3, $4, $5, $6, $7, $8, $9) RETURNING id;

-- name: TicketByHash :one
SELECT * FROM object_capabilities_named WHERE token_hash = $1 AND kind = $2;

-- name: TicketByIDForUpdate :one
SELECT o.*, u.username FROM object_capabilities o JOIN users u ON u.principal_id = o.principal_id WHERE o.id = $1 FOR UPDATE OF o;

-- name: ConsumeTicket :execrows
UPDATE object_capabilities SET kind = sqlc.arg(kind), delegation_id = sqlc.arg(delegation_id),
resource_type = sqlc.arg(resource_type), resource_id = sqlc.arg(resource_id), constraints_json = sqlc.arg(constraints_json),
token_hash = sqlc.arg(token_hash), created_at = sqlc.arg(created_at), expires_at = sqlc.arg(expires_at),
consumed_at = sqlc.narg(consumed_at), revoked_at = sqlc.narg(revoked_at)
WHERE id=sqlc.arg(id) AND consumed_at IS NULL AND revoked_at IS NULL;

-- name: RevokeChangeTickets :execrows
UPDATE object_capabilities SET revoked_at = sqlc.arg(revoked_at)::timestamptz
WHERE kind = sqlc.arg(kind) AND resource_type = 'change' AND resource_id = sqlc.arg(resource_id) AND revoked_at IS NULL AND consumed_at IS NULL;

-- ===== the sweep ======================================================

-- name: DeleteExpiredConsents :execrows
DELETE FROM oauth_consents WHERE expires_at <= $1;

-- name: DeleteSpentCodes :execrows
DELETE FROM oauth_codes WHERE used_at IS NOT NULL OR expires_at < $1;

-- name: DeleteDeadTokens :execrows
DELETE FROM oauth_tokens WHERE expires_at < sqlc.arg(cutoff) OR (revoked_at IS NOT NULL AND revoked_at < sqlc.arg(cutoff));

-- name: DeleteDeadTickets :execrows
DELETE FROM object_capabilities WHERE expires_at < sqlc.arg(cutoff) OR (consumed_at IS NOT NULL AND consumed_at < sqlc.arg(cutoff));

-- ===== git pull tokens ================================================

-- name: PullTokenByHash :one
SELECT * FROM git_pull_tokens WHERE token_hash = $1;

-- name: InsertPullToken :exec
INSERT INTO git_pull_tokens (token_hash, project_id, created_at) VALUES ($1, $2, $3);

-- name: DeletePullToken :execrows
DELETE FROM git_pull_tokens WHERE project_id=$1;

-- name: ApproverGroups :many
SELECT m.group_name FROM group_members m JOIN groups g ON g.name=m.group_name
WHERE m.principal_id=(SELECT u.principal_id FROM users u WHERE u.username = sqlc.arg(username)) AND m.revoked_at IS NULL
AND (m.expires_at IS NULL OR m.expires_at>sqlc.arg(at)::timestamptz)
AND g.retired_at IS NULL ORDER BY m.group_name;

-- name: GroupsOfPrincipal :many
SELECT m.group_name FROM group_members m JOIN groups g ON g.name=m.group_name
WHERE m.principal_id=sqlc.arg(principal)::uuid AND m.revoked_at IS NULL
AND (m.expires_at IS NULL OR m.expires_at>sqlc.arg(at)::timestamptz)
AND g.retired_at IS NULL ORDER BY m.group_name;

-- name: SaveDelegation :one
UPDATE delegations SET client_id = sqlc.arg(client_id), client_name = sqlc.arg(client_name),
audience = sqlc.arg(audience), kind = sqlc.arg(kind), scopes_json = sqlc.arg(scopes_json), group_bounds_json = sqlc.arg(group_bounds_json),
project_bounds_json = sqlc.arg(project_bounds_json), auth_time = sqlc.narg(auth_time), auth_method = sqlc.arg(auth_method),
created_at = sqlc.arg(created_at), last_used = sqlc.narg(last_used), expires_at = sqlc.narg(expires_at), revoked_at = sqlc.narg(revoked_at)
WHERE id=sqlc.arg(id) RETURNING id;

-- name: TicketByID :one
SELECT * FROM object_capabilities_named WHERE id = $1 AND kind = $2;

-- ===== invites ========================================================

-- name: InsertInvite :one
INSERT INTO invites (id, username, principal_id, label, role, invited_by, invited_by_principal, invited_by_via, token_hash, created_at, expires_at, purpose)
VALUES (sqlc.arg(id), sqlc.arg(username), sqlc.arg(principal_id), sqlc.arg(label), sqlc.arg(role), sqlc.arg(invited_by), sqlc.arg(invited_by_principal), sqlc.arg(invited_by_via), sqlc.arg(token_hash), sqlc.arg(created_at), sqlc.arg(expires_at), sqlc.arg(purpose)) RETURNING *;

-- name: InviteByHash :one
SELECT * FROM invites WHERE token_hash = $1;

-- name: InviteByHashForUpdate :one
SELECT * FROM invites WHERE token_hash = $1 FOR UPDATE;

-- name: InviteByIDForUpdate :one
SELECT * FROM invites WHERE id = $1 FOR UPDATE;

-- An invite expires by AppTown's clock (at), which wrote its expiry and
-- judges it at acceptance.
-- name: PendingInvites :many
SELECT * FROM invites WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at > sqlc.arg(at)::timestamptz ORDER BY id;

-- PendingInvitesForUpdate is a person's pending links: by principal, or by
-- name for a link made before principals.
-- name: PendingInvitesForUpdate :many
SELECT * FROM invites WHERE (principal_id = sqlc.narg(principal_id) OR (principal_id IS NULL AND username = sqlc.arg(username)))
  AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE;

-- name: SettleInvite :execrows
UPDATE invites SET accepted_at = sqlc.narg(accepted_at), revoked_at = sqlc.narg(revoked_at), revoked_by = sqlc.arg(revoked_by), revoked_by_principal = sqlc.arg(revoked_by_principal), revoked_by_via = sqlc.arg(revoked_by_via)
WHERE id=sqlc.arg(id) AND accepted_at IS NULL AND revoked_at IS NULL;

-- ===== scoped system bindings (bindings.go) ===========================

-- name: InsertSystemBinding :one
INSERT INTO system_bindings (id, purpose, project_id, granted_by, granted_by_principal, granted_by_via, decision, created_at) VALUES (sqlc.arg(id), sqlc.arg(purpose), sqlc.arg(project_id), sqlc.arg(granted_by), sqlc.arg(granted_by_principal), sqlc.arg(granted_by_via), sqlc.arg(decision), sqlc.arg(created_at)) RETURNING id;

-- name: SystemBinding :one
SELECT * FROM system_bindings WHERE id = $1;

-- name: RevokeSystemBinding :execrows
UPDATE system_bindings SET revoked_at = sqlc.arg(at)::timestamptz, revoked_by = sqlc.arg(revoked_by), revoked_by_principal = sqlc.arg(revoked_by_principal), revoked_by_via = sqlc.arg(revoked_by_via), revoked_why = sqlc.arg(revoked_why)
WHERE id=sqlc.arg(id) AND revoked_at IS NULL;

-- PrincipalsNow is each principal as it stands: its name, whether it was
-- removed, and whether its account is disabled.
-- name: PrincipalsNow :many
SELECT p.id, p.name, (p.removed_at IS NOT NULL)::boolean AS removed, COALESCE(u.disabled_at IS NOT NULL, false)::boolean AS disabled
FROM principals p LEFT JOIN users u ON u.principal_id = p.id
WHERE p.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: UserByPrincipalForUpdate :one
SELECT * FROM users WHERE principal_id = $1 FOR UPDATE;

-- name: UserByPrincipal :one
SELECT * FROM users WHERE principal_id = $1;

-- ===== external sign-in =================================================
-- name: ExternalProviderCurrent :one
SELECT * FROM external_signin_providers WHERE current_slot;
-- name: ExternalProviderByID :one
SELECT * FROM external_signin_providers WHERE id=$1;
-- name: SaveExternalProvider :exec
INSERT INTO external_signin_providers(id,current_slot,revision,issuer,display_name,client_id,secret_ref,callback_origin,enabled,allow_signup,initial_group,created_by,updated_by,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT(id) DO UPDATE SET current_slot=EXCLUDED.current_slot,revision=EXCLUDED.revision,display_name=EXCLUDED.display_name,secret_ref=EXCLUDED.secret_ref,callback_origin=EXCLUDED.callback_origin,enabled=EXCLUDED.enabled,allow_signup=EXCLUDED.allow_signup,initial_group=EXCLUDED.initial_group,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at;
-- name: ExternalBindingBySubject :one
SELECT * FROM external_signin_bindings WHERE provider_id=$1 AND issuer=$2 AND subject=$3;
-- name: ExternalBindings :many
SELECT * FROM external_signin_bindings WHERE provider_id=$1 ORDER BY created_at,id;
-- name: InsertExternalBinding :exec
INSERT INTO external_signin_bindings(id,provider_id,issuer,subject,principal_id,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: RevokeExternalBinding :execrows
UPDATE external_signin_bindings SET revoked_at=sqlc.arg(at)::timestamptz WHERE id=$1 AND revoked_at IS NULL;
-- name: InsertExternalTransaction :exec
INSERT INTO external_signin_transactions(id,state_hash,provider_id,provider_revision,verifier_hash,nonce_hash,purpose,local_session_id,principal_id,proposed_name,callback,return_path,created_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14);
-- name: ConsumeExternalTransaction :one
UPDATE external_signin_transactions SET consumed_at=sqlc.arg(at)::timestamptz
WHERE state_hash=sqlc.arg(state_hash) AND verifier_hash=sqlc.arg(verifier_hash) AND consumed_at IS NULL AND expires_at>sqlc.arg(at)::timestamptz RETURNING *;
-- name: InsertExternalSession :exec
INSERT INTO external_signin_sessions(session_id,binding_id,provider_id,provider_revision,authenticated_at) VALUES($1,$2,$3,$4,$5);
-- name: IsExternalSession :one
SELECT EXISTS(SELECT 1 FROM external_signin_sessions WHERE session_id=$1)::boolean;
-- name: SweepExternalTransactions :exec
DELETE FROM external_signin_transactions WHERE expires_at<=sqlc.arg(at)::timestamptz;
-- name: ExternalProviderEnabled :one
SELECT EXISTS(SELECT 1 FROM external_signin_providers WHERE enabled AND current_slot)::boolean;
-- name: PersistentLocalRoots :one
SELECT count(*)::bigint FROM users u WHERE u.disabled_at IS NULL AND u.password_hash<>'' AND EXISTS (
 SELECT 1 FROM grants g WHERE g.role_name='instance-administrator' AND g.capability='' AND g.scope_type='instance'
 AND g.revoked_at IS NULL AND g.expires_at IS NULL
 AND ((g.subject_type='user' AND g.subject=u.principal_id::text) OR (g.subject_type='group' AND
 (g.subject='all-authenticated-users' OR EXISTS(SELECT 1 FROM group_members m WHERE m.group_name=g.subject AND m.principal_id=u.principal_id AND m.revoked_at IS NULL AND m.expires_at IS NULL))))
);
-- name: ExternalBindingByID :one
SELECT * FROM external_signin_bindings WHERE id=$1;
-- name: ExternalBindingsForPrincipal :many
SELECT * FROM external_signin_bindings WHERE principal_id=$1 ORDER BY created_at,id;
