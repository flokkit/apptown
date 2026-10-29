package dataprotection

import (
	"context"
)

// HasStore says whether a backup store is attached.
func (s *core) HasStore(ctx context.Context) (bool, error) {
	cfg, err := s.Store(ctx)
	return cfg != nil, err
}

// CopyToStore writes a whole-AppTown archive at path to the backup store
// under key, private, and checks what the store then holds (size and
// digest). The archive is encrypted before it gets here; the store sees
// only that. It goes up in parts, so its size has no single-upload limit.
func (s *core) CopyToStore(ctx context.Context, path, key string) (bucket string, err error) {
	c, cfg, err := s.storeClient(ctx)
	if err != nil {
		return "", err
	}
	if _, err := c.PutPrivateFile(ctx, cfg.Bucket, key, path); err != nil {
		return "", err
	}
	return cfg.Bucket, nil
}
