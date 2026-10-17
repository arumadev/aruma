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

	"github.com/osama300/aurma-project/internal/app"
	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/log"
	"github.com/osama300/aurma-project/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: aruma [--config file] <command>

commands:
  serve            run the server
  migrate up       apply pending database migrations
  migrate status   show the migration version of each schema
  config check     validate the configuration and exit
  version          print the version

The configuration is an optional YAML file (--config or ARUMA_CONFIG), overridden by
ARUMA_* environment variables. See aruma.example.yaml.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "aruma:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
		if s.Pending() {
			state = "pending"
		}
		fmt.Fprintf(stdout, "%-8s version %d/%d  %s\n", s.Schema, s.Current, s.Latest, state)
	}
	return nil
}
