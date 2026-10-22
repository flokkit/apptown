// Package digitalocean is the DigitalOcean App Platform target driver
// (design/subsystems/deployment.md §12A): the renderer, the reconciler, the
// managed-database need module and the account inventory, with every
// collaborator injected. AppTown keeps the connector, the token's resolution
// and the doors.
//
// The driver works through the API end to end: the provider is a
// cloud-account connector holding the name of the token secret; a target is
// a region under it; the app is the deployment's handle; the registry the
// images are pushed to is created on first use.
package digitalocean
