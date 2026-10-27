package api

import "testing"

// SetIndexBatch makes every batch of s's index paths n results, and runs after, when not nil,
// between a batch and the next snapshot.
func SetIndexBatch(s *Service, n int, after func()) { s.indexBatch, s.afterBatch = n, after }

// ObserveIndexPeak tells f, after each query an index path serves, the most bytes of documents
// it held at once.
func ObserveIndexPeak(s *Service, f func(int)) { s.observePeak = f }

// SetMaxChangedBytes bounds what an index path keeps of the documents that change while it runs
// to n, until the test ends.
func SetMaxChangedBytes(t testing.TB, n int) {
	old := maxChangedBytes
	maxChangedBytes = n
	t.Cleanup(func() { maxChangedBytes = old })
}
