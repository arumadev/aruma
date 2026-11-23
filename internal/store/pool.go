package store

import (
	"context"
	"log/slog"
	"net/url"
	"regexp"
	"runtime"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The pool's default size: four connections per CPU the process may use, at least 16 and at
// most 64. One pool serves every service and project: the Document Database holds a connection
// per commit in flight (up to documents.limits.commit_concurrency per database), the Realtime
// Database one per database whose writer commits, and Auth, Storage and the console one per
// request. pgx's own default, max(4, CPUs), queued a busy project's commits behind each other
// and made the quiet ones wait: on 4 CPUs with four projects, 32 connections gave 43% more
// Document Database writes than 4 and halved the latency the busy project added to the others.
// The connections open on demand, so a quiet server holds few.
const (
	minDefaultMaxConns = 16
	maxDefaultMaxConns = 64
)

// DefaultMaxConns is the pool size Open uses when the URL does not set pool_max_conns.
func DefaultMaxConns() int32 {
	return int32(min(max(minDefaultMaxConns, 4*runtime.GOMAXPROCS(0)), maxDefaultMaxConns))
}

// dedicatedConns are the connections a server holds besides its pool: the single-instance locks
// of the Realtime Database and of the Document Database. A migrator (aruma migrate) holds one
// more for its lock and its own pool's connections while it runs.
const dedicatedConns = 2

// keywordMaxConns finds pool_max_conns in a keyword/value connection string.
var keywordMaxConns = regexp.MustCompile(`(^|\s)pool_max_conns\s*=`)

// setsMaxConns reports whether the connection string sets pool_max_conns itself.
func setsMaxConns(databaseURL string) bool {
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		u, err := url.Parse(databaseURL)
		return err == nil && u.Query().Has("pool_max_conns")
	}
	return keywordMaxConns.MatchString(databaseURL)
}

// CheckPoolSize logs the pool's size and where it comes from, and warns when Postgres accepts
// fewer connections than the pool and the dedicated connections may open together: past
// max_connections, less the slots reserved for superusers and for roles with
// pg_use_reserved_connections, a new connection fails and the request that needed it with it.
// databaseURL is the one the pool was opened with.
func CheckPoolSize(ctx context.Context, pool *pgxpool.Pool, databaseURL string, log *slog.Logger) {
	size := pool.Config().MaxConns
	source := "default"
	if setsMaxConns(databaseURL) {
		source = "pool_max_conns"
	}
	var maxConns, reserved int
	err := pool.QueryRow(ctx, `SELECT current_setting('max_connections')::int,
		current_setting('superuser_reserved_connections')::int + coalesce(current_setting('reserved_connections', true), '0')::int`).Scan(&maxConns, &reserved)
	if err != nil {
		log.Info("database pool", "max_conns", size, "source", source)
		log.Warn("database pool: could not read max_connections", "error", err)
		return
	}
	log.Info("database pool", "max_conns", size, "source", source, "postgres_max_connections", maxConns)
	if need, have := int(size)+dedicatedConns, maxConns-reserved; need > have {
		log.Warn("database pool: Postgres's max_connections leaves fewer connections than this server may open; raise max_connections or set a smaller pool_max_conns in database_url",
			"max_conns", size, "dedicated", dedicatedConns, "postgres_available", have)
	}
}
