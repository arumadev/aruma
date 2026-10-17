package store

import (
	"context"
	"errors"
	"fmt"
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

// Backup writes a custom-format dump of the database to out.
func Backup(ctx context.Context, tools PGTools, databaseURL, out string) error {
	bin, err := tools.path("pg_dump")
	if err != nil {
		return err
	}
	env, err := pgEnv(databaseURL)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--format=custom", "--no-owner", "--no-privileges", "--file="+out)
	return run(cmd, env)
}

// Restore replaces the database's contents with a dump made by Backup. Objects in the dump are
// dropped and recreated, all in one transaction, so a failed restore changes nothing.
func Restore(ctx context.Context, tools PGTools, databaseURL, in string) error {
	if _, err := os.Stat(in); err != nil {
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

// pgEnv passes the connection through PG* variables rather than arguments, so the password
// never shows in the process list.
func pgEnv(databaseURL string) (connEnv, error) {
	cfg, err := pgconn.ParseConfig(databaseURL)
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
	if cfg.TLSConfig == nil {
		vars = append(vars, "PGSSLMODE=disable")
	}
	return connEnv{vars: vars, database: cfg.Database}, nil
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
