// Package deploy runs a described application on a target and says what
// it saw: the app contract, the driver contract every target kind
// implements, and the drivers themselves in the packages under this one.
// It keeps no records and knows nothing of AppTown; whoever calls it hands
// it a Placement and a Source and keeps the Observation that comes back.
// The records of what is deployed where, and their settlement, belong to
// internal/apptown/deployment, which calls this package.
package deploy
