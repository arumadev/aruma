// Command aruma is the Aruma server and its operator commands.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/log"
	"github.com/arumadev/aruma/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: aruma [--config file] <command>

commands:
  serve                           run the server
  migrate up|status               apply or show database migrations
  accounts create --email E       create a project owner account (password from the terminal or stdin)
  accounts disable|enable --email E
  accounts reset-password --email E
  backup --out FILE               dump the database with pg_dump
  restore --in FILE --yes         replace the database with a backup (stop the server first)
  config check                    validate the configuration and exit
  version                         print the version

The configuration is an optional YAML file (--config or ARUMA_CONFIG), overridden by
ARUMA_* environment variables. See aruma.example.yaml. backup and restore use pg_dump and
pg_restore from PATH, or from ARUMA_PG_BIN.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		var apiErr *apierr.Error
		if errors.As(err, &apiErr) {
			err = errors.New(apiErr.Message) // the operator needs the explanation, not the code
		}
		fmt.Fprintln(os.Stderr, "aruma:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("aruma", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	configPath := fs.String("config", os.Getenv("ARUMA_CONFIG"), "path to the YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing command")
	}
	load := func() (*config.Config, error) { return config.Load(*configPath, os.Getenv) }

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "serve":
		cfg, err := load()
		if err != nil {
			return err
		}
		return app.Serve(ctx, cfg, log.New(stdout, cfg.LogLevel), version)
	case "migrate":
		return migrate(ctx, args[1:], stdout)
	case "accounts":
		cfg, err := load()
		if err != nil {
			return err
		}
		return accounts(ctx, cfg, args[1:], stdin, stdout, stderr)
	case "backup":
		return backup(ctx, args[1:], stdout, stderr)
	case "restore":
		return restore(ctx, args[1:], stdout, stderr)
	case "config":
		if len(args) != 2 || args[1] != "check" {
			return errors.New("usage: aruma config check")
		}
		if _, err := load(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "configuration is valid")
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// migrate needs only the database URL, so it works before the rest of the configuration exists.
func migrate(ctx context.Context, args []string, stdout io.Writer) error {
	url := os.Getenv("ARUMA_DATABASE_URL")
	if url == "" {
		return errors.New("ARUMA_DATABASE_URL is not set")
	}
	if len(args) != 1 {
		return errors.New("usage: aruma migrate up|status")
	}
	switch args[0] {
	case "up":
		if err := store.Migrate(ctx, url); err != nil {
			return err
		}
		return printStatus(ctx, url, stdout)
	case "status":
		return printStatus(ctx, url, stdout)
	default:
		return fmt.Errorf("unknown migrate command %q", args[0])
	}
}

func printStatus(ctx context.Context, url string, stdout io.Writer) error {
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	statuses, err := store.Status(ctx, pool)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		state := "up to date"
		switch {
		case s.Pending():
			state = "pending"
		case s.Ahead():
			state = "newer than this binary"
		}
		fmt.Fprintf(stdout, "%-8s version %d/%d  %s\n", s.Schema, s.Current, s.Latest, state)
	}
	return nil
}
