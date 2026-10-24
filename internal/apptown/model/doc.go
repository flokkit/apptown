// Package model selects model patterns from project and instance settings and
// limits concurrent agent work per provider binding.
//
// # Selection
//
// A slot names a purpose, such as implement, review or chat. A profile selects an
// instance mapping for that purpose; an undeclared profile is standard. A request
// containing a slash is already a provider/model pattern and bypasses slot lookup.
// An empty request selects the default slot.
//
// [Selection.Resolve] reads the current project profile and delegates to
// [Slots.Resolve]. The latter tries a project override, the instance mapping for
// the project's profile, then the standard profile. It repeats that lookup for
// fallback slots before using the configured instance default. Its returned slot
// and profile identify the chosen mapping for caller-owned execution evidence.
// Only an absent mapping passes the lookup on; a failed read is returned as an
// error, never answered with a less specific mapping or "no model". Changing
// mappings affects later resolutions, not old receipts.
//
// [Slots.Set] commits a mapping change and its audit receipt together. These APIs
// do not authorize a caller; the application adapter must check permission first.
// The actor passed to Set supplies receipt attribution only.
//
// # Execution capacity
//
// [Scheduler.Acquire] reserves capacity for a binding and returns a release
// function. Callers release once after execution. [BindingCapacity] reads the
// current integration configuration: an explicit positive concurrency wins;
// custom endpoints otherwise get one slot and hosted providers eight. A pattern
// no binding names uses the scheduler's configured default; a capacity that
// cannot be read refuses the acquisition.
//
// Capacity controls concurrent execution, not monetary spending. Waiting is not
// cancellable or guaranteed FIFO. Resizing creates a new pool without draining
// holders of the old pool, so the new limit is not an instantaneous global cap.
// Daemon shutdown has its own bounded wait.
//
// # Related owners
//
// Integration owns provider bindings. Inference owns discovery, allowed models and
// provider credentials; resolving a pattern does not authorize an inference call.
// Harness configuration and chat model pins belong to their execution owners.
// Selecting a model does not select a harness.
package model
