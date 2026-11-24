package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
)

// Backups use the official pg_dump and pg_restore in the custom format: compressed, portable
// across machines, and restorable selectively. The database is assumed to belong to Aruma.

// PGTools locates pg_dump and pg_restore: in dir when set (ARUMA_PG_BIN), else on PATH.
type PGTools struct{ Dir string }

func (t PGTools) path(name string) (string, error) {
	if t.Dir != "" {
		p := filepath.Join(t.Dir, name)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s not found in %s", name, t.Dir)
		}
		return p, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH; install the PostgreSQL client tools or set ARUMA_PG_BIN", name)
	}
	return p, nil
}

// Backup writes a custom-format dump of the database to out. The file holds every table, so it
// is created readable by its owner only.
func Backup(ctx context.Context, tools PGTools, databaseURL, out string) error {
	return BackupSnapshot(ctx, tools, databaseURL, out, "")
}

// BackupSnapshot is Backup of the database as a transaction that exported snapshot sees
// (pg_export_snapshot), so what is read next to the dump in that transaction agrees with it.
// An empty snapshot is a dump of its own.
func BackupSnapshot(ctx context.Context, tools PGTools, databaseURL, out, snapshot string) error {
	bin, err := tools.path("pg_dump")
	if err != nil {
		return err
	}
	env, err := pgEnv(databaseURL)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil { // an existing file keeps its mode otherwise
		f.Close()
		return err
	}
	f.Close()
	args := []string{"--format=custom", "--no-owner", "--no-privileges", "--file=" + out}
	if snapshot != "" {
		args = append(args, "--snapshot="+snapshot)
	}
	return run(exec.CommandContext(ctx, bin, args...), env)
}

// Restore replaces the database's contents with a dump made by Backup. Objects in the dump are
// dropped and recreated, all in one transaction, so a failed restore changes nothing. It
// refuses while an Aruma server is connected to the database: a server keeps state of the
// database in memory (the Realtime Database's tree and listeners, the Document Database's
// listeners, Storage's binding to its directory) that a restore under it would contradict.
func Restore(ctx context.Context, tools PGTools, databaseURL, in string) error {
	if _, err := os.Stat(in); err != nil {
		return err
	}
	if err := refuseWhileServed(ctx, databaseURL); err != nil {
		return err
	}
	bin, err := tools.path("pg_restore")
	if err != nil {
		return err
	}
	env, err := pgEnv(databaseURL)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--single-transaction", "--exit-on-error", "--dbname="+env.database, in)
	return run(cmd, env)
}

type connEnv struct {
	vars     []string
	database string
}

// pgParams maps connection parameters of the URL's query to libpq's environment variables.
// Parameters outside this list are refused rather than silently dropped, so pg_dump never
// connects with weaker settings than the server does.
var pgParams = map[string]string{
	"sslmode":          "PGSSLMODE",
	"sslrootcert":      "PGSSLROOTCERT",
	"sslcert":          "PGSSLCERT",
	"sslkey":           "PGSSLKEY",
	"sslnegotiation":   "PGSSLNEGOTIATION",
	"connect_timeout":  "PGCONNECT_TIMEOUT",
	"application_name": "PGAPPNAME",
}

// clientParams are parameters of the URL that set up pgx's pool or its client side, not the
// connection (the operations guide suggests pool_max_conns): pg_dump and pg_restore open one
// connection and have nothing to apply them to.
var clientParams = map[string]bool{
	"pool_max_conns": true, "pool_min_conns": true, "pool_min_idle_conns": true,
	"pool_max_conn_lifetime": true, "pool_max_conn_lifetime_jitter": true, "pool_max_conn_idle_time": true,
	"pool_health_check_period": true, "statement_cache_capacity": true, "description_cache_capacity": true,
	"default_query_exec_mode": true,
}

// pgEnv passes the connection through PG* variables rather than arguments, so the password
// never shows in the process list.
func pgEnv(databaseURL string) (connEnv, error) {
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return connEnv{}, fmt.Errorf("database url: %w", err)
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return connEnv{}, fmt.Errorf("database url: %w", err)
	}
	vars := append(os.Environ(),
		"PGHOST="+cfg.Host,
		"PGPORT="+strconv.Itoa(int(cfg.Port)),
		"PGUSER="+cfg.User,
		"PGDATABASE="+cfg.Database,
	)
	if cfg.Password != "" {
		vars = append(vars, "PGPASSWORD="+cfg.Password)
	}
	for k, v := range u.Query() {
		if clientParams[k] {
			continue
		}
		env, ok := pgParams[k]
		if !ok {
			return connEnv{}, fmt.Errorf("database url: parameter %q is not supported by backup and restore", k)
		}
		vars = append(vars, env+"="+v[len(v)-1])
	}
	if !u.Query().Has("sslmode") {
		vars = append(vars, "PGSSLMODE=prefer") // libpq's and pgx's shared default
	}
	return connEnv{vars: vars, database: cfg.Database}, nil
}

// refuseWhileServed fails when another session of an Aruma process (Open's application_name)
// is connected to the database.
func refuseWhileServed(ctx context.Context, databaseURL string) error {
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	var n int
	var addr string
	err = pool.QueryRow(ctx, `SELECT count(*), COALESCE(min(host(client_addr)), 'a local socket') FROM pg_stat_activity
		WHERE datname = current_database() AND application_name = 'aruma' AND pid <> pg_backend_pid()`).Scan(&n, &addr)
	if err != nil {
		return fmt.Errorf("check for a running server: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("an Aruma server is connected to this database (%d connections, from %s): stop it before the restore, and start it after", n, addr)
	}
	return nil
}

func run(cmd *exec.Cmd, env connEnv) error {
	cmd.Env = env.vars
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("%s failed: %s", filepath.Base(cmd.Path), out)
		}
		return err
	}
	return nil
}
