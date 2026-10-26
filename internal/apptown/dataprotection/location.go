package dataprotection

import "path/filepath"

// DiskDirectory is the directory for an instance's local backup sets and
// staging files. It preserves the supplied data directory's relative or
// absolute form; it does not relocate existing sets or resolve their paths.
func DiskDirectory(dataDir string) string { return filepath.Join(dataDir, "backups") }

// BackupDirectory is where this service keeps backup sets on AppTown's disk.
// Sets in the backup store retain their recorded object keys instead.
func (s *Service) BackupDirectory() string { return DiskDirectory(s.dataDir) }
