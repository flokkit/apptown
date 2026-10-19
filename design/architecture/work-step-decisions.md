# Manual Work decisions

Ordinary approval and override enter `stepDecisions` with a verified `Principal`
and a typed request. HTTP parses input and translates errors; it does not own the
approver policy. There is no public actor-string approval method. Any transport
resolves identity before calling this command, as HTTP does.

The command reads the exact step/run/project/change, attempt, run revision, current
stage, step type and configuration. Its Access adapter checks the current account,
approver expression and capability before a write transaction opens. The private
store locks the project, run and step in the normal Work order and compares that
snapshot. A changed configuration, scope, attempt or stage requires a fresh request.
Authorization does not hold a pool connection while consulting its collaborators.
Permission changes racing an already completed authorization observation retain the
same decision-time semantics as other commands; this is not a global revocation lock.

Eligibility preserves the administrator exemption and the segregation of the
change author. Empty step approvers use the project default; named people, active
groups and active scoped roles are supported. Revoked/expired memberships and retired
groups do not satisfy a group expression. Required fact reads return errors rather
than substituting an empty author or role. Malformed grant conditions cannot become
an unconditional grant. Console and run instruments cannot approve or override;
OAuth scopes only subtract from the person's authority.

Current vote evidence is read under the lock. It is deliberately absent from the
pre-authorization fence: two distinct authorized people can contribute to the same
quorum concurrently. The canonical approval model rejects duplicate votes, closed
rounds and votes during rework. The vote, evidence, required receipt and any rework
intent commit together. Dispatch and advancement occur only afterward. Conflicts
are HTTP 409, invalid commands 400, denied authority 403 and failed required writes
500. Storage errors are not presented as a person's invalid vote.

Accepted-Work inheritance is a separate command with its own source/landing proof;
see [Work landing acceptance](work-landing-acceptance.md). It uses the same private
vote transaction participant without pretending to be a newly authenticated person.
