package preview

import (
	"sync"

	"flokkit.io/forge/internal/deploy/docker"
)

// servedRecord is what a preview this forge booted serves: its incarnation,
// the commit it was booted from ("" for a change's folder, which is not a
// commit), and the public prepared values it was booted with, its contract
// among them. A runtime check judges a preview against this record, never
// against today's settings (design/operations/workers.md, "Previews on the
// worker").
type servedRecord struct {
	// made numbers the record among all records, as they are made: a claim
	// forgets only records made before it began.
	made        int64
	Incarnation string
	Source      string
	Public      docker.PublicPrepared
}

// servedPreviews holds the records by preview name, in memory: a forge
// that starts again discards every preview, and their records with them.
// Each name also counts the forge's decisions to discard it: a boot keeps
// its record only if no discard was decided since it began, so a discard
// wins over a boot already in flight, and the reconciliation removes what
// that boot left.
type servedPreviews struct {
	m        sync.Map
	mu       sync.Mutex
	discards map[string]int
	made     int64 // the last record's number
}

func (s *Service) forgetServed(name string) { s.served.m.Delete(name) }

// discardDecided forgets a preview's record and counts the decision.
func (s *Service) discardDecided(name string) {
	s.served.mu.Lock()
	defer s.served.mu.Unlock()
	if s.served.discards == nil {
		s.served.discards = map[string]int{}
	}
	s.served.discards[name]++
	s.served.m.Delete(name)
}

// discardsSoFar is how many discards of a preview were decided: a boot
// takes it as it begins, and records its preview only if it is unchanged.
func (s *Service) discardsSoFar(name string) int {
	s.served.mu.Lock()
	defer s.served.mu.Unlock()
	return s.served.discards[name]
}

// recordServedSince keeps a boot's record unless a discard was decided
// since the boot took discards, and says whether it did.
func (s *Service) recordServedSince(name string, discards int, r servedRecord) bool {
	s.served.mu.Lock()
	defer s.served.mu.Unlock()
	if s.served.discards[name] != discards {
		return false
	}
	s.served.made++
	r.made = s.served.made
	s.served.m.Store(name, r)
	return true
}

// recordsMadeSoFar is the number of the last record made: a claim takes it
// as it begins.
func (s *Service) recordsMadeSoFar() int64 {
	s.served.mu.Lock()
	defer s.served.mu.Unlock()
	return s.served.made
}

// forgetRecordsMadeBy forgets every record made up to made: those a claim
// that began after them removed. A record made later, by a boot the claim
// left alone, stays.
func (s *Service) forgetRecordsMadeBy(made int64) {
	s.served.mu.Lock()
	defer s.served.mu.Unlock()
	s.served.m.Range(func(name, v any) bool {
		if v.(servedRecord).made <= made {
			s.served.m.Delete(name)
		}
		return true
	})
}

func (s *Service) servedRecordOf(name string) (servedRecord, bool) {
	v, ok := s.served.m.Load(name)
	if !ok {
		return servedRecord{}, false
	}
	return v.(servedRecord), true
}
