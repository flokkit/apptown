package dataprotection

import (
	"context"
	"fmt"
	"os"
)

// storeObjectLimit is the largest object the store takes in one upload
// (S3's and Spaces' single PUT); the store client does not split one.
const storeObjectLimit = 5 << 30

// HasStore says whether a backup store is attached.
func (s *core) HasStore(ctx context.Context) (bool, error) {
	cfg, err := s.Store(ctx)
	return cfg != nil, err
}

// CopyToStore writes a whole-AppTown archive at path to the backup store
// under key, private, and checks what the store then holds (size and
// digest). The archive is encrypted before it gets here; the store sees
// only that. An archive over the store's single upload is refused by name.
func (s *core) CopyToStore(ctx context.Context, path, key string) (bucket string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > storeObjectLimit {
		return "", fmt.Errorf("the archive is %d bytes, over the %d the backup store takes in one upload; copy it off the host yourself", fi.Size(), int64(storeObjectLimit))
	}
	c, cfg, err := s.storeClient(ctx)
	if err != nil {
		return "", err
	}
	if _, err := c.PutPrivateFile(ctx, cfg.Bucket, key, path); err != nil {
		return "", err
	}
	return cfg.Bucket, nil
}
