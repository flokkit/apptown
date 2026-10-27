// Package supervisor owns one registered host installation's external control
// journal and restart fence. Its state survives replacement of the daemon and
// its database/data files. Registration is not recovery or replacement readiness.
//
// Only host administration enrolls an installation. The daemon's authenticated
// control connection can stage an intention, not authorize a stop or install.
// Matched recovery and the final current-authority handoff are separate required
// participants; their absence never grants permission to replace the daemon.
package supervisor
