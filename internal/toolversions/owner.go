package toolversions

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Snapshot is one tool versions list as a unit of work uses it: the list,
// its runtime images and its identity, the update's name and the digest of
// what was read. It is never changed after it is made.
type Snapshot struct {
	Name, Digest string
	Images       Images
	list         *List
}

// HasLine says whether the snapshot's list names tool at line.
func (s Snapshot) HasLine(tool, line string) bool { return s.list != nil && s.list.HasLine(tool, line) }

// HasTool says whether the snapshot's list names tool at any line.
func (s Snapshot) HasTool(tool string) bool { return s.list != nil && s.list.HasTool(tool) }

// RunImage is the run image the snapshot's list names for tool at line;
// false when it names none.
func (s Snapshot) RunImage(tool, line string) (string, bool) {
	if s.list == nil {
		return "", false
	}
	img := s.list.Tools[tool][line].Images["run"]
	return img, img != ""
}

// NewSnapshot validates a list's document and makes its snapshot, named
// name; the digest is of raw.
func NewSnapshot(name string, raw []byte) (Snapshot, error) {
	l, err := Parse(raw)
	if err != nil {
		return Snapshot{}, err
	}
	im, err := l.Images()
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(raw)
	return Snapshot{Name: name, Digest: "sha256:" + hex.EncodeToString(sum[:]), Images: im, list: l}, nil
}

// Source is what an owner that runs images asks: the current snapshot.
type Source interface {
	Current() Snapshot
}

// Versions owns a process's tool versions: the current validated list.
type Versions struct {
	mu      sync.RWMutex
	current Snapshot
}

// New is an owner holding s.
func New(s Snapshot) *Versions { return &Versions{current: s} }

// Current is the list in use, as a snapshot a unit of work keeps.
func (v *Versions) Current() Snapshot {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.current
}

// HasLine says whether the current list names tool at line.
func (v *Versions) HasLine(tool, line string) bool { return v.Current().HasLine(tool, line) }

// HasTool says whether the current list names tool at any line.
func (v *Versions) HasTool(tool string) bool { return v.Current().HasTool(tool) }

// Install puts a newer validated list in place: work that starts from now
// on uses it, work under way keeps its snapshot. The seam for importing
// tool versions updates; nothing in this release calls it.
func (v *Versions) Install(s Snapshot) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.current = s
}
