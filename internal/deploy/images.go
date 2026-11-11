package deploy

import "context"

// ImageStore identifies the actual shared store before any deletion. Unknown
// identity or inventory is an error, never an empty store. TrimImages removes
// only recorded expired references, excluding keep, and reports refused deletes.
type ImageStore interface {
	ImageStore(context.Context, Placement) (string, error)
	TrimImages(context.Context, Placement, []string, []string) ([]string, []Left, error)
}

// ImageCollector reclaims blobs after manifest deletion. The owner calls this
// only after checking that no release using the account is in flight.
type ImageCollector interface {
	CollectionStore(context.Context) (string, error)
	CollectImages(context.Context) error
}

// ImagePresence checks the selected store without consulting the hub. Missing
// is false; an unassessable store is an error and never authorizes a rebuild.
type ImagePresence interface {
	HasImages(context.Context, Placement, []string) (bool, error)
}
