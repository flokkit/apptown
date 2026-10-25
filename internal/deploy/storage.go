package deploy

import (
	"context"
	"time"
)

// StorageBinding is observed evidence of the engine that may open one volume.
// It does not grant ownership of the volume or permission to delete it.
type StorageBinding struct {
	Need          string    `json:"need"`
	Volume        string    `json:"volume"`
	DataPath      string    `json:"dataPath"`
	DeclaredImage string    `json:"declaredImage"`
	ImageID       string    `json:"imageID"`
	RepoDigest    string    `json:"repoDigest,omitempty"`
	VolumeCreated time.Time `json:"volumeCreated"`
	VolumeDriver  string    `json:"volumeDriver"`
	Origin        string    `json:"origin"`
}

// StorageBindings records evidence before an engine can write. Establish is
// idempotent for identical evidence and refuses to replace an existing binding.
// Retire follows confirmed volume deletion; it never authorizes deletion.
type StorageBindings interface {
	Get(context.Context, string, string) (StorageBinding, bool, error)
	Establish(context.Context, StorageBinding) error
	Retire(context.Context, string, string) error
}

// StorageRestore is supplied only by an admitted whole-instance logical restore.
// It replaces source certification with this boot's newly created target and
// preserves both in the owner's audit transaction.
type StorageRestore interface {
	Image(need, declared string, source *StorageBinding) (image string, exact bool, err error)
	Restore(context.Context, *StorageBinding, StorageBinding) error
}

// StorageCopy identifies a verified backup item admitted for an engine restore.
// A physical copy requires immutable engine evidence captured at export time.
type StorageCopy struct {
	Volume        string
	Physical      bool
	EngineImageID string
}
