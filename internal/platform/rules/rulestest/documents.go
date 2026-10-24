package rulestest

import (
	"context"
	"strings"
	"sync"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// MemoryDocuments is a rules.Documents held in memory, per project. It counts every read, so a
// test can check that a condition is evaluated once, that the lookup limits hold, and that the
// request's project is the one that reaches Documents.
//
// A document is held as the value get() gives the rules. Paths are written as the oracle's
// mocks write them, /databases/(default)/documents/...; the leading slash is optional.
type MemoryDocuments struct {
	// Missing is what a read of a document that is neither held nor marked absent answers: nil
	// for "not found", or an error such as rules.ErrNoDocuments.
	Missing error

	mu    sync.Mutex
	docs  map[docKey]docEntry
	reads map[docKey]int
}

type docKey struct {
	project int64
	after   bool
	path    string // the segments joined by "/", without a leading slash
}

type docEntry struct {
	doc   rules.Value
	found bool
	err   error
}

// NewMemoryDocuments returns an empty MemoryDocuments in which every document is not found.
func NewMemoryDocuments() *MemoryDocuments {
	return &MemoryDocuments{docs: map[docKey]docEntry{}, reads: map[docKey]int{}}
}

func key(project int64, path string, after bool) docKey {
	return docKey{project: project, after: after, path: strings.TrimPrefix(path, "/")}
}

// Put holds doc at path, as the state before the request's writes or, with after, after them.
func (m *MemoryDocuments) Put(project int64, path string, after bool, doc rules.Value) {
	m.set(key(project, path, after), docEntry{doc: doc, found: true})
}

// Absent marks the document at path as not found, whatever Missing says.
func (m *MemoryDocuments) Absent(project int64, path string, after bool) {
	m.set(key(project, path, after), docEntry{})
}

// Fail makes a read of the document at path fail with err.
func (m *MemoryDocuments) Fail(project int64, path string, after bool, err error) {
	m.set(key(project, path, after), docEntry{err: err})
}

func (m *MemoryDocuments) set(k docKey, e docEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[k] = e
}

// Get implements rules.Documents.
func (m *MemoryDocuments) Get(_ context.Context, project int64, path []string, after bool) (rules.Value, bool, error) {
	k := docKey{project: project, after: after, path: strings.Join(path, "/")}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads[k]++
	e, ok := m.docs[k]
	if !ok {
		return rules.Value{}, false, m.Missing
	}
	return e.doc, e.found, e.err
}

// Reads returns how many reads reached a project, all documents together.
func (m *MemoryDocuments) Reads(project int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, c := range m.reads {
		if k.project == project {
			n += c
		}
	}
	return n
}

// ReadsOf returns how many reads reached one document.
func (m *MemoryDocuments) ReadsOf(project int64, path string, after bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads[key(project, path, after)]
}
