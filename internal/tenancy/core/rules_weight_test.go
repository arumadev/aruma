package core

import (
	"testing"

	"github.com/arumadev/aruma/internal/store/storetest"
)

// What compiles is kept (the review's SR3-02): a ruleset whose literal patterns nearly use
// regex_bytes, the heaviest the engine compiles, fits in the cache, and the next request does
// not compile it again.
func TestTheHeaviestRulesetIsKept(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	heavy := "'x'.matches('a{1000}b{1000}c{1000}d{1000}e{1000}f{1000}g{1000}h{1000}i{1000}j{1}') || 'x'.matches('a{1000}b{1000}c{1000}d{1000}e{1000}f{1000}g{1000}h{1000}i{1000}j{2}')"
	publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, storageFile(heavy))
	rs, err := s.CompiledRelease(t.Context(), proj.Number, StorageRelease)
	if err != nil {
		t.Fatal(err)
	}
	c := s.compiledCache()
	c.mu.Lock()
	_, kept := c.entries[releaseKey{proj.Number, StorageRelease}]
	used := c.used
	c.mu.Unlock()
	if !kept || used != rs.Weight() || rs.Weight() > s.opts.RulesLimits.CompiledRulesetsBytes/4 {
		t.Errorf("a ruleset of %d bytes: kept %v, the cache holds %d of %d", rs.Weight(), kept, used, c.max)
	}
	if again, _ := s.CompiledRelease(t.Context(), proj.Number, StorageRelease); again != rs {
		t.Error("the next request compiled it again")
	}
}
