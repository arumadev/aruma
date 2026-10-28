package core

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// Usage counts what the dashboard's usage screen shows, per project and hour (ADR-030 §8): the
// documents read, written and deleted, counted as Firestore bills them; the peaks of snapshot
// listeners (Listen targets) and of active connections (Listen streams); and the security
// rules' allows, denies and errors. A request adds to counters in memory, under a read lock, and
// never writes a row: Run adds the counts to documents.usage_hours every few seconds, in one
// statement, and once more when it stops, so that they survive a restart, and prunes the hours
// older than UsageRetention. A nil *Usage counts nothing.
type Usage struct {
	q   *queries.Queries
	now func() time.Time
	log *slog.Logger

	// mu guards hours: a count holds it shared, Flush exclusively while it takes them.
	mu    sync.RWMutex
	hours map[usageKey]*usageCounts
	// gauges are the projects' current listeners and connections, which outlive a flush. A
	// gauge changes under mu held shared, so that seed, which holds it exclusively, can drop
	// the gauges back at zero.
	gauges sync.Map // int64 → *usageGauge

	// flushMu orders the flushes, and guards pruned.
	flushMu sync.Mutex
	pruned  time.Time
}

// UsageRetention is how long the hours are kept: the dashboard's longest period, 30 days, and a day.
const UsageRetention = 31 * 24 * time.Hour

// UsageFlushInterval is how often Run adds the counts to the table.
const UsageFlushInterval = 10 * time.Second

// UsageHour is a project's usage in one hour.
type UsageHour struct {
	Hour            time.Time
	Reads           int64
	Writes          int64
	Deletes         int64
	ListenersPeak   int64
	ConnectionsPeak int64
	RulesAllows     int64
	RulesDenies     int64
	RulesErrors     int64
}

// RuleOutcome is how the rules decided a request, as the usage counts it.
type RuleOutcome uint8

const (
	RuleAllow RuleOutcome = iota
	RuleDeny
	// RuleError is a decision the rules could not make: the request was refused UNAVAILABLE.
	RuleError
)

type usageKey struct {
	project int64
	hour    int64 // the hour's start, Unix seconds
}

type usageCounts struct {
	reads, writes, deletes                atomic.Int64
	listenersPeak, connectionsPeak        atomic.Int64
	rulesAllows, rulesDenies, rulesErrors atomic.Int64
}

type usageGauge struct {
	listeners, connections atomic.Int64
}

// NewUsage returns the usage counters, kept in pool's documents.usage_hours. now is the clock;
// nil is time.Now.
func NewUsage(pool *pgxpool.Pool, now func() time.Time, log *slog.Logger) *Usage {
	if now == nil {
		now = time.Now
	}
	return &Usage{q: queries.New(pool), now: now, log: cmp.Or(log, slog.Default()), hours: map[usageKey]*usageCounts{}}
}

func hourOf(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

// add runs f on project's counters of the current hour.
func (u *Usage) add(project int64, f func(*usageCounts)) {
	key := usageKey{project, hourOf(u.now()).Unix()}
	u.mu.RLock()
	if c := u.hours[key]; c != nil {
		f(c)
		u.mu.RUnlock()
		return
	}
	u.mu.RUnlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.hours[key]
	if c == nil {
		c = new(usageCounts)
		u.hours[key] = c
	}
	f(c)
}

func raise(peak *atomic.Int64, n int64) {
	for {
		old := peak.Load()
		if n <= old || peak.CompareAndSwap(old, n) {
			return
		}
	}
}

// Read counts n documents read.
func (u *Usage) Read(project int64, n int) {
	if u == nil || n <= 0 {
		return
	}
	u.add(project, func(c *usageCounts) { c.reads.Add(int64(n)) })
}

// Wrote counts a commit's writes: n documents written, and deletes deleted.
func (u *Usage) Wrote(project int64, writes []Write) {
	if u == nil {
		return
	}
	var n, d int64
	for _, w := range writes {
		switch w.Kind {
		case WriteDelete:
			d++
		case WriteVerify:
		default:
			n++
		}
	}
	if n == 0 && d == 0 {
		return
	}
	u.add(project, func(c *usageCounts) {
		c.writes.Add(n)
		c.deletes.Add(d)
	})
}

// Rule counts one decision of the security rules.
func (u *Usage) Rule(project int64, o RuleOutcome) {
	if u == nil {
		return
	}
	u.add(project, func(c *usageCounts) {
		switch o {
		case RuleAllow:
			c.rulesAllows.Add(1)
		case RuleDeny:
			c.rulesDenies.Add(1)
		default:
			c.rulesErrors.Add(1)
		}
	})
}

func (u *Usage) gauge(project int64) *usageGauge {
	g, _ := u.gauges.LoadOrStore(project, new(usageGauge))
	return g.(*usageGauge)
}

// Listeners adds delta to project's open snapshot listeners.
func (u *Usage) Listeners(project int64, delta int) {
	if u == nil || delta == 0 {
		return
	}
	u.mu.RLock()
	n := u.gauge(project).listeners.Add(int64(delta))
	u.mu.RUnlock()
	if delta > 0 {
		u.add(project, func(c *usageCounts) { raise(&c.listenersPeak, n) })
	}
}

// Connections adds delta to project's open connections.
func (u *Usage) Connections(project int64, delta int) {
	if u == nil || delta == 0 {
		return
	}
	u.mu.RLock()
	n := u.gauge(project).connections.Add(int64(delta))
	u.mu.RUnlock()
	if delta > 0 {
		u.add(project, func(c *usageCounts) { raise(&c.connectionsPeak, n) })
	}
}

func (c *usageCounts) hour(hour int64) UsageHour {
	return UsageHour{
		Hour: time.Unix(hour, 0).UTC(), Reads: c.reads.Load(), Writes: c.writes.Load(), Deletes: c.deletes.Load(),
		ListenersPeak: c.listenersPeak.Load(), ConnectionsPeak: c.connectionsPeak.Load(),
		RulesAllows: c.rulesAllows.Load(), RulesDenies: c.rulesDenies.Load(), RulesErrors: c.rulesErrors.Load(),
	}
}

// merge adds h's counts to c, and raises its peaks to h's.
func (c *usageCounts) merge(h UsageHour) {
	c.reads.Add(h.Reads)
	c.writes.Add(h.Writes)
	c.deletes.Add(h.Deletes)
	raise(&c.listenersPeak, h.ListenersPeak)
	raise(&c.connectionsPeak, h.ConnectionsPeak)
	c.rulesAllows.Add(h.RulesAllows)
	c.rulesDenies.Add(h.RulesDenies)
	c.rulesErrors.Add(h.RulesErrors)
}

func (h UsageHour) zero() bool { return h == UsageHour{Hour: h.Hour} }

// seed starts the current hour's peaks at the listeners and connections still open, so that an
// hour in which none opened shows them, and drops the gauges with none open. u.mu is held.
func (u *Usage) seed() {
	hour := hourOf(u.now()).Unix()
	u.gauges.Range(func(k, v any) bool {
		g := v.(*usageGauge)
		l, c := g.listeners.Load(), g.connections.Load()
		if l <= 0 && c <= 0 {
			// Below zero only for a project Forget dropped while its streams were open.
			u.gauges.Delete(k)
			return true
		}
		key := usageKey{k.(int64), hour}
		cs := u.hours[key]
		if cs == nil {
			cs = new(usageCounts)
			u.hours[key] = cs
		}
		raise(&cs.listenersPeak, l)
		raise(&cs.connectionsPeak, c)
		return true
	})
}

// Forget drops what project counted and has not flushed, and its listeners and connections, so
// that no later flush writes a row for it: the purge of a deleted project (PurgeProject) calls it
// before it deletes the project's hours. A flush under way finishes first.
func (u *Usage) Forget(project int64) {
	if u == nil {
		return
	}
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	maps.DeleteFunc(u.hours, func(k usageKey, _ *usageCounts) bool { return k.project == project })
	u.gauges.Delete(project)
}

// Flush adds the counts so far to the table, in one statement, and prunes the hours past
// UsageRetention once an hour. Counts it could not add are kept for the next.
func (u *Usage) Flush(ctx context.Context) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	u.mu.Lock()
	taken := u.hours
	u.hours = make(map[usageKey]*usageCounts, len(taken))
	u.seed()
	u.mu.Unlock()

	var p queries.AddUsageHoursParams
	for k, c := range taken {
		h := c.hour(k.hour)
		if h.zero() {
			continue
		}
		p.ProjectNumbers = append(p.ProjectNumbers, k.project)
		p.Hours = append(p.Hours, h.Hour)
		p.Reads = append(p.Reads, h.Reads)
		p.Writes = append(p.Writes, h.Writes)
		p.Deletes = append(p.Deletes, h.Deletes)
		p.ListenersPeaks = append(p.ListenersPeaks, h.ListenersPeak)
		p.ConnectionsPeaks = append(p.ConnectionsPeaks, h.ConnectionsPeak)
		p.RulesAllows = append(p.RulesAllows, h.RulesAllows)
		p.RulesDenies = append(p.RulesDenies, h.RulesDenies)
		p.RulesErrors = append(p.RulesErrors, h.RulesErrors)
	}
	if len(p.ProjectNumbers) > 0 {
		if err := u.q.AddUsageHours(ctx, p); err != nil {
			u.mu.Lock()
			for k, c := range taken {
				dst := u.hours[k]
				if dst == nil {
					dst = new(usageCounts)
					u.hours[k] = dst
				}
				dst.merge(c.hour(k.hour))
			}
			u.mu.Unlock()
			return err
		}
	}
	if now := u.now(); now.Sub(u.pruned) >= time.Hour {
		if _, err := u.q.PruneUsageHours(ctx, hourOf(now).Add(-UsageRetention)); err != nil {
			return err
		}
		u.pruned = now
	}
	return nil
}

// Run flushes every interval (UsageFlushInterval when zero) until ctx ends, then once more.
func (u *Usage) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(cmp.Or(interval, UsageFlushInterval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := u.Flush(fctx); err != nil {
				u.log.Error("documents: the last usage flush", "error", err)
			}
			return
		case <-t.C:
			if err := u.Flush(ctx); err != nil && ctx.Err() == nil {
				u.log.Error("documents: usage flush", "error", err)
			}
		}
	}
}

// Hours returns project's usage from since's hour on, oldest first: the hours stored and the
// counts not yet flushed. It never reads another project's.
func (u *Usage) Hours(ctx context.Context, project int64, since time.Time) ([]UsageHour, error) {
	rows, err := u.q.ListUsageHours(ctx, queries.ListUsageHoursParams{ProjectNumber: project, Since: hourOf(since)})
	if err != nil {
		return nil, err
	}
	byHour := make(map[int64]*usageCounts, len(rows))
	at := func(hour int64) *usageCounts {
		c := byHour[hour]
		if c == nil {
			c = new(usageCounts)
			byHour[hour] = c
		}
		return c
	}
	for _, r := range rows {
		at(r.Hour.Unix()).merge(UsageHour(r))
	}
	from := hourOf(since).Unix()
	u.mu.RLock()
	for k, c := range u.hours {
		if k.project == project && k.hour >= from {
			at(k.hour).merge(c.hour(k.hour))
		}
	}
	u.mu.RUnlock()
	if v, ok := u.gauges.Load(project); ok {
		g := v.(*usageGauge)
		c := at(hourOf(u.now()).Unix())
		raise(&c.listenersPeak, g.listeners.Load())
		raise(&c.connectionsPeak, g.connections.Load())
	}
	out := make([]UsageHour, 0, len(byHour))
	for _, hour := range slices.Sorted(maps.Keys(byHour)) {
		if h := byHour[hour].hour(hour); !h.zero() {
			out = append(out, h)
		}
	}
	return out, nil
}
