// Command aruma is the Aruma server and its operator commands.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/osama300/aurma-project/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: aruma <command>

commands:
  migrate up       apply pending database migrations
  migrate status   show the migration version of each schema
  version          print the version

environment:
  ARUMA_DATABASE_URL   Postgres connection string (required for migrate)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aruma:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	case "migrate":
		return migrate(ctx, args[1:])
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func migrate(ctx context.Context, args []string) error {
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
		return printStatus(ctx, url)
	case "status":
		return printStatus(ctx, url)
	default:
		return fmt.Errorf("unknown migrate command %q", args[0])
	}
}

func printStatus(ctx context.Context, url string) error {
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
		fmt.Printf("%-8s version %d/%d  %s\n", s.Schema, s.Current, s.Latest, state)
	}
	return nil
}
