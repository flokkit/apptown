package wow

import (
	"fmt"
	"io/fs"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// The starter content a pack hands a new project — its seed folder and its
// toolchain file — is stock, read from the ways-of-working update this
// AppTown shipped with (Dependencies.Stock), never from a compiled-in copy.

// Seed is the seed folder of a pack's starter (stock/seeds/<pack>): nil when
// the pack has none.
func (s *Service) Seed(pack string) fs.FS {
	if s.deps.Stock == nil || pack == "" {
		return nil
	}
	if _, err := fs.Stat(s.deps.Stock, "seeds/"+pack); err != nil {
		return nil
	}
	sub, err := fs.Sub(s.deps.Stock, "seeds/"+pack)
	if err != nil {
		return nil
	}
	return sub
}

// Toolchain is a stock toolchain file (stock/toolchains/<name>.toml).
func (s *Service) Toolchain(name string) ([]byte, error) {
	if s.deps.Stock == nil {
		return nil, fmt.Errorf("no stock toolchain %q: %w", name, fs.ErrNotExist)
	}
	b, err := fs.ReadFile(s.deps.Stock, "toolchains/"+name+".toml")
	if err != nil {
		return nil, fmt.Errorf("no stock toolchain %q: %w", name, fs.ErrNotExist)
	}
	return b, nil
}

// StarterIsStatic says whether a pack's starter app, as its seed's
// .apptown/app.yaml declares it, is static files only: no part runs a server
// and nothing is needed, so a database or cache would have nothing to serve.
// A pack whose seed declares no contract (or no seed) says nothing: false.
func (s *Service) StarterIsStatic(pack string) bool {
	seed := s.Seed(pack)
	if seed == nil {
		return false
	}
	raw, err := fs.ReadFile(seed, ".apptown/app.yaml")
	if err != nil {
		return false
	}
	c, err := appcontract.Parse(raw)
	if err != nil || len(c.Needs) > 0 || len(c.Init) > 0 || len(c.Parts) == 0 {
		return false
	}
	for _, part := range c.Parts {
		if !part.IsStatic() {
			return false
		}
	}
	return true
}
