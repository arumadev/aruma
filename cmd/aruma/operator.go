package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/log"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// accounts manages project owner accounts from the server (U-19): create, disable, enable
// and reset-password. Passwords are read from the terminal without echo, or from stdin.
func accounts(ctx context.Context, cfg *config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: aruma accounts create|disable|enable|reset-password --email <email>")
	}
	fs := flag.NewFlagSet("accounts "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "account email (required)")
	name := fs.String("display-name", "", "display name (create)")
	locale := fs.String("locale", "ar", "console language, ar or en (create)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("--email is required")
	}

	pool, err := store.Open(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckCurrent(ctx, pool); err != nil {
		return err
	}
	a, err := app.New(cfg, log.New(stderr, "warn"), version, pool)
	if err != nil {
		return err
	}
	svc := a.Service

	switch args[0] {
	case "create":
		pw, err := readPassword(stdin, stderr, true)
		if err != nil {
			return err
		}
		acc, err := svc.CreateAccount(ctx, core.NewAccount{Email: *email, Password: pw, DisplayName: *name, Locale: *locale})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created account %s (%s)\n", acc.Email, acc.ID)
	case "disable", "enable":
		if err := svc.SetDisabled(ctx, *email, args[0] == "disable"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "account %s %sd\n", *email, args[0])
	case "reset-password":
		pw, err := readPassword(stdin, stderr, true)
		if err != nil {
			return err
		}
		if err := svc.ResetPassword(ctx, *email, pw); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "password reset for %s; its sessions were signed out and its CLI tokens revoked\n", *email)
	default:
		return fmt.Errorf("unknown accounts command %q", args[0])
	}
	return nil
}

// readPassword prompts twice on a terminal, or reads one line from a pipe.
func readPassword(stdin io.Reader, stderr io.Writer, confirm bool) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "Password: ")
		a, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		if confirm {
			fmt.Fprint(stderr, "Repeat password: ")
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stderr)
			if err != nil {
				return "", err
			}
			if string(a) != string(b) {
				return "", errors.New("passwords do not match")
			}
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// backup and restore wrap pg_dump and pg_restore; they need only the database URL, and backup
// the Storage data directory.
func backup(ctx context.Context, cfg *config.Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "file to write the database dump to (required)")
	storageOut := fs.String("storage-out", "", "file to write the Storage files the dump names to, a tar.gz")
	storageDir := fs.String("storage-dir", cfg.Storage.DataDir, "the Storage data directory (default storage.data_dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("usage: aruma backup --out <file> [--storage-out <file>]")
	}
	url := cfg.DatabaseURL.Reveal()
	tools := store.PGTools{Dir: os.Getenv("ARUMA_PG_BIN")}
	if *storageOut == "" {
		if err := store.Backup(ctx, tools, url, *out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "backup written to %s\n", *out)
		if cfg.Storage.Enabled {
			fmt.Fprintln(stderr, "aruma: Storage is on and its files are not in this backup: pass --storage-out to write them with the dump")
		}
		return nil
	}
	if *storageDir == "" || !filepath.IsAbs(*storageDir) {
		return errors.New("--storage-out needs the absolute path of the Storage data directory: storage.data_dir (ARUMA_STORAGE_DATA_DIR) or --storage-dir")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := storage.Backup(ctx, pool, *storageDir, *storageOut, func(ctx context.Context, snapshot string) error {
		return store.BackupSnapshot(ctx, tools, url, *out, snapshot)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "backup written to %s, and %d Storage files (%d bytes) to %s\n", *out, rep.Files, rep.Bytes, *storageOut)
	if len(rep.Missing) > 0 {
		fmt.Fprintf(stderr, "aruma: %d files the database names were not in %s (first: %s); the restored database names them too, without their bytes\n",
			len(rep.Missing), *storageDir, rep.Missing[0])
	}
	return nil
}

func restore(ctx context.Context, url string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "backup file to restore (required)")
	yes := fs.Bool("yes", false, "confirm that the database's current contents will be replaced")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("usage: aruma restore --in <file> --yes")
	}
	if !*yes {
		return errors.New("restore replaces the database's current contents; stop the server and pass --yes to confirm")
	}
	if err := store.Restore(ctx, store.PGTools{Dir: os.Getenv("ARUMA_PG_BIN")}, url, *in); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "restored %s\n", *in)
	return nil
}
