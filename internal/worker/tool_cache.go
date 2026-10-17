package worker

import (
	"io/fs"
	"path/filepath"

	"flokkit.io/forge/internal/execution"
)

// The tool cache is one folder on the worker that every task, check and
// chat container mounts writable at execution.ToolCacheMount, where the
// forge's environment points mise's download cache (MISE_CACHE_DIR). A
// project's declared toolchain is installed in every container that needs
// it; without a cache shared between them, each install asked GitHub's API
// again for the same release listings and ran into its anonymous rate limit.
//
// What it holds is what mise fetched: release listings, archives, and the
// lock files mise itself takes so that concurrent installs do not tear
// one another's files. A tool's installation is never in it: installs stay
// in each container (MISE_DATA_DIR), so a package one project installs into
// its Python is not another project's. It is not a channel for anything
// else the worker holds: no tree, record, credential or generated file is
// written there. It is writable by every container that mounts it, so a
// project could leave files there that another project's container could
// read; mise verifies the archives it installs where the tool publishes
// checksums or attestations.

// toolCacheMounts are the mount every task's and check's container gets.
func (s *Server) toolCacheMounts() []string {
	if s.ToolCache == "" {
		return nil
	}
	return []string{ToolCacheVolume(s.ToolCache)}
}

// ToolCacheVolume is the mount argument that presents dir, writable, at
// execution.ToolCacheMount.
func ToolCacheVolume(dir string) string { return dir + ":" + execution.ToolCacheMount }

// ToolCacheSize is how many bytes the files under dir take, as far as they
// could be read.
func ToolCacheSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
