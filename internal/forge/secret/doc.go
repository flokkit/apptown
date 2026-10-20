// Package secret owns stored values, application secret declarations, deployment
// profiles and bindings, and runtime provider resolution. Callers authorize access
// and select the project, deployment and source tree; these storage and resolution
// APIs do not establish caller authority.
//
// # Application delivery
//
// [Service.Resolve] reads SecretSpec revision 1.0 from the selected workspace,
// validates consumer scopes against the app contract, chooses a profile, and
// resolves each declared value. [Spec] describes logical names and consumers;
// [Bindings] maps a project/deployment/name to a store reference. Repository provider
// coordinates are reported as ignored instead of controlling the store.
//
// Resolution uses an explicit binding, a declaration default, or a generated value
// when requested. Generated values and their bindings may be persisted during
// resolution; the whole operation is not one transaction. Missing required values
// return [MissingError]. Do not use a partial [Delivery] after an error.
//
// [Delivery] contains plaintext environment values by part or init consumer, plus
// metadata receipts without values. No scopes means all consumers receive all
// selected secrets. The app contract supports environment delivery and rejects a
// file channel. SecretSpec's as_path is separately reported as ignored and still
// uses environment delivery.
//
// [Service.Check] is the same judgement without effects: a generated secret not
// yet bound counts as provided rather than being minted, values are read only to
// prove they exist, and nothing is returned or persisted. Deployment uses it before
// a release is built; Resolve judges again at delivery. Resolve and Check read
// bindings and the built-in store strictly: a store or binding read that fails is
// [UnavailableError] (unknown), never a missing value. A declaration that cannot
// be used is fault.Invalid.
//
// [Service.ProfileFor] and [Service.BindingsFor] log read failures and fall back.
// [Service.Stored] reports lookup errors as absent. They are conveniences for
// display, not for delivery.
//
// [TestsEnv] gives an app's tests step the declaration's generated secrets as
// throwaway values minted per call, and its declared defaults. It reads no
// binding or store, and names the secrets it withholds.
//
// # Stores and runtime resolution
//
// [Vault] encrypts values with a key in the same database's secret_store_key table.
// It protects against accidental disclosure, not database compromise. Preserve the
// key when restoring values; moving it during a migration does not rotate it.
// Application Infisical bindings use [Service.InfisicalValues], without local file
// overrides. A cached provider value may remain available after refresh failure.
//
// Daemon/runtime lookup uses [Service.WithContext]: Infisical values are overlaid
// by local secrets.env entries. Ordinary provider errors allow fallback; context
// cancellation and deadline errors propagate. [Service.StepEnv] selects only STEP_
// entries from that map and removes the prefix. Other owners can deliver separately
// authorized integration or SCM credentials; StepEnv is not their permission check.
//
// # Instance secrets
//
// AppTown's own credentials (a target's API token, an SSH key, a registry
// password) are named by targets and connectors and read by runtime lookup.
// [Service.SetInstance] and [Service.RemoveInstance] are the write-only door
// to the bootstrap file for them: the caller authorizes an administrator, the
// value is one line and never returned, logged or recorded, and each change
// leaves a receipt (secret.instance-set, secret.instance-removed) naming who
// acted on which name. A name Infisical holds is refused rather than shadowed,
// and an Infisical that cannot be read (a failed refresh included, whatever
// it last cached) refuses the write as unknown. [Service.InstanceNames] lists
// the names with the store each is read from; unlike runtime lookup it does
// not fall back, so an unreadable secrets file or Infisical is an error
// (unknown), never an empty list.
//
// [Vault.Derived] makes a value from the store's key for one scope and name,
// stored nowhere: the need credentials of a deployment, a preview or a
// rehearsal (deployment.NeedCredentials). It is as protected as the store's
// own values and no more; a value changes only when the key does.
//
// Delivery exposes values to its intended process. Provider-model credential
// containment belongs to Inference. This package does not provide tenant isolation,
// a general rotation service or additional cloud-secret backends.
package secret
