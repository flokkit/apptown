# External sign-in

AppTown can add one external OpenID Connect provider to its internal sign-in page. Local passwords, invitations, password recovery and CLI tokens remain available. Trusted-proxy sign-in is a separate mode.

An administrator registers an authorization-code client at the provider with the exact callback shown in People → External sign-in. AppTown requires an explicitly configured public origin. Production endpoints use HTTPS and system certificate trust; explicit HTTP development origins permit loopback endpoints only. A self-hosted HTTPS issuer can use private network addresses. Discovery, token and key requests have deadlines and size limits and refuse redirects.

Store the client secret through the existing instance-secret operation, for example `apptown secret set OIDC_CLIENT_SECRET < secret-file`. Configure its name, not its value, in external sign-in settings. Saving configuration disables it. Test discovery, then explicitly enable it. A successful discovery test does not prove user authentication or correct provider-client credentials.

Keep an enabled local-password administrator with nonexpiring local authority. AppTown refuses activation or account/grant changes that would remove this recovery route while the provider is enabled. Provider outage does not remove local password sign-in. The host operator's existing password reset remains available.

## Account association

An external identity is its configured provider connection, issuer and stable subject. Email, display name and suggested username never select an existing account. Administrators can bind an exact subject to an existing local principal UUID. The binding is recorded; revocation does not transfer it to another person.

A person can link from Your account while signed in with a local password session. Linking asks for the local password and recent provider authentication. The provider must supply an authentication time within five minutes. Issuing a new token from an old SSO session does not establish that freshness. If the provider cannot supply it, an administrator can use the explicit binding operation instead.

New-account creation is disabled by default. An administrator can explicitly enable it and select an existing initial local group. A new verified subject chooses an unused AppTown account name; name collisions require a different name. This policy grants membership through AppTown's own identity owner. Provider claims do not dynamically create roles or permissions.

## Session and revocation boundaries

External browser sessions last at most one hour, bounded by local session policy. Renewal performs a new provider authentication exchange; ordinary renewal may reuse the provider's SSO session. AppTown does not retain access or refresh tokens for local login.

Provider disable or configuration revision, binding revocation, account disable, local logout and session expiry refuse further external-session access. The same check applies to consequential work that retained that session's credential reference. Local password sessions and independently issued CLI tokens are not external sessions and retain their own revocation rules.

Upstream deprovisioning is observed on fresh authentication, not instantaneous back-channel revocation. Disable the local account if all local access must end. Revoking an external identity alone does not revoke an independently issued local token.

State, PKCE and nonce bind each callback to one browser flow. A flow expires after ten minutes and is consumed once before the code exchange. A failed or interrupted exchange requires starting again. Starting another flow replaces the browser's pending flow. Callbacks return only to admitted local paths; request Host or forwarded headers cannot change the registered callback.

## Provider setup

- Authentik: create an OAuth2/OpenID provider and application, select authorization-code flow, and register the exact callback. Use the provider's exact issuer from discovery.
- Okta: create a web OIDC integration with authorization-code support and its exact organization/authorization-server issuer. Register the exact callback and client authentication method.
- Entra ID: use a tenant-specific v2 issuer, not a common or organizations issuer template. Register a web redirect URI and client secret.
- Google: create a web OAuth client and register the callback; use `https://accounts.google.com` as issuer. Stable subjects remain identity keys even when email changes.

These configuration patterns describe the supported protocol. An installation must test its own registered client and account policy. AppTown's application SSO registration and household-provider deployment are distinct features; configuring AppTown sign-in does not register clients for hosted applications.

## Command-line administration

`apptown signin show` reads configuration and revision. `configure --file provider.json` saves disabled configuration; the JSON contains `revision`, `issuer`, `displayName`, `clientId`, `secretRef`, `allowSignup` and `initialGroup`. An initial configuration uses revision zero. Changes use the revision returned by `show`.

`apptown signin test` observes discovery. `enable --revision N` and `disable --revision N` make explicit state changes. `bindings` lists current connection bindings. `bind --provider UUID --principal UUID --subject SUBJECT` associates an exact provider identity; `revoke BINDING_UUID` revokes that association. Commands use current administrator authority and never accept a provider token or secret value as an argument.
