package core

import (
	"sync"
	"time"
)

// Limits are the server's capacity limits (ADR-019). Path depth and length are fixed
// protocol limits (path.go).
type Limits struct {
	// MessageBytes caps one WebSocket message, so one write from an SDK.
	MessageBytes int64
	// UnauthenticatedMessageBytes caps a message from a session that has not signed in.
	UnauthenticatedMessageBytes int64
	// InflightBytes caps the bytes of partly received messages across connections.
	InflightBytes int64
	// ConnectionsPerIP caps the open connections from one client address.
	ConnectionsPerIP int
	// RESTWriteBytes caps one REST write.
	RESTWriteBytes int64
	// StringBytes caps one string value.
	StringBytes int64
	// WriteBytesPerMinute caps the bytes written to one database per minute.
	WriteBytesPerMinute int64
	// ConnectionsPerProject caps the open connections to one project's databases.
	ConnectionsPerProject int
	// OutboxBytes caps the data waiting to be sent on one connection.
	OutboxBytes int64
	// ReadRows caps the stored rows (leaves, and priorities of objects) one read loads on
	// the database's writer: a get, the first data of a listen, the value a conditional
	// write compares. A larger read is refused with too_big, before it is loaded.
	ReadRows int64
}

// DefaultLimits are Firebase's published limits where a developer sees them.
var DefaultLimits = Limits{
	MessageBytes:                16 << 20,
	UnauthenticatedMessageBytes: 1 << 20,
	InflightBytes:               512 << 20,
	ConnectionsPerIP:            500,
	RESTWriteBytes:              16 << 20,
	StringBytes:                 10 << 20,
	WriteBytesPerMinute:         64 << 20,
	ConnectionsPerProject:       10_000,
	OutboxBytes:                 64 << 20,
	ReadRows:                    1_000_000,
}

// rateWindow counts bytes per minute in two fixed windows, weighting the previous one by how
// much of it still overlaps the last minute. It is the writer's, so it needs no lock.
type rateWindow struct {
	limit     int64
	start     time.Time
	cur, prev int64
}

// allow reports whether n more bytes fit in the last minute at now, and counts them if so.
func (w *rateWindow) allow(now time.Time, n int64) bool {
	if w.limit <= 0 {
		return true
	}
	switch elapsed := now.Sub(w.start); {
	case elapsed >= 2*time.Minute:
		w.start, w.cur, w.prev = now, 0, 0
	case elapsed >= time.Minute:
		w.start, w.prev, w.cur = w.start.Add(time.Minute), w.cur, 0
	}
	overlap := 1 - float64(now.Sub(w.start))/float64(time.Minute)
	used := w.cur + int64(float64(w.prev)*overlap)
	if used+n > w.limit {
		return false
	}
	w.cur += n
	return true
}

// counter counts open connections per project.
type counter struct {
	mu    sync.Mutex
	limit int
	n     map[int64]int
}

// acquire takes one slot for project, or reports that none is free.
func (c *counter) acquire(project int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[project] >= c.limit {
		return false
	}
	c.n[project]++
	return true
}

func (c *counter) release(project int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[project]--; c.n[project] <= 0 {
		delete(c.n, project)
	}
}
