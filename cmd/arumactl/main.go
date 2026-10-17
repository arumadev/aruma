// Command arumactl is the developer's command-line client for an Aruma server. It signs in
// with a personal CLI token created in the console.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: arumactl [--json] <command>

  login --server URL                      store a CLI token (read from the terminal or stdin)
  logout                                  forget the stored token
  projects list
  projects create ID [--name NAME]
  projects get ID
  apps list --project P [--platform web|ios|android]
  apps create --project P --platform web|ios|android [--name N] [--package PKG] [--bundle ID]
  apps sdkconfig --project P APP_ID [--out FILE]     print or save the app's config file
  apikeys list --project P
  apikeys create --project P [--name N]
  apikeys rotate --project P KEY_UID
  sa list --project P
  sa create --project P ACCOUNT_ID [--name N]
  sa keys create --project P SERVICE_ACCOUNT --out FILE   SERVICE_ACCOUNT is its id or email
  version

ARUMA_SERVER and ARUMA_TOKEN override the stored login.
`

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "arumactl:", err)
		os.Exit(1)
	}
}

type cli struct {
	env    func(string) string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	json   bool
}

func run(args []string, env func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("arumactl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	asJSON := fs.Bool("json", false, "print raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := &cli{env: env, stdin: stdin, stdout: stdout, stderr: stderr, json: *asJSON}
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing command")
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "login":
		return c.login(args[1:])
	case "logout":
		return c.logout()
	}
	creds, err := loadCredentials(env)
	if err != nil {
		return err
	}
	api := newClient(creds)
	if len(args) < 2 {
		fs.Usage()
		return fmt.Errorf("%s: missing subcommand", args[0])
	}
	switch args[0] + " " + args[1] {
	case "projects list":
		return c.projectsList(api)
	case "projects create":
		return c.projectsCreate(api, args[2:])
	case "projects get":
		return c.projectsGet(api, args[2:])
	case "apps list":
		return c.appsList(api, args[2:])
	case "apps create":
		return c.appsCreate(api, args[2:])
	case "apps sdkconfig":
		return c.appsSDKConfig(api, args[2:])
	case "apikeys list":
		return c.keysList(api, args[2:])
	case "apikeys create":
		return c.keysCreate(api, args[2:])
	case "apikeys rotate":
		return c.keysRotate(api, args[2:])
	case "sa list":
		return c.saList(api, args[2:])
	case "sa create":
		return c.saCreate(api, args[2:])
	case "sa keys":
		if len(args) > 2 && args[2] == "create" {
			return c.saKeysCreate(api, args[3:])
		}
	}
	fs.Usage()
	return fmt.Errorf("unknown command %q", strings.Join(args, " "))
}

// flags parses command flags. Positional arguments may come before or after the flags.
func (c *cli) flags(name string, args []string, define func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	define(fs)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func (c *cli) login(args []string) error {
	var server string
	if _, err := c.flags("login", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", c.env("ARUMA_SERVER"), "server URL, e.g. https://aruma.example.com")
	}); err != nil {
		return err
	}
	if server == "" {
		return errors.New("--server is required")
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid server URL %q", server)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		fmt.Fprintln(c.stderr, "warning: this server uses plain http; the token will cross the network unencrypted")
	}
	token, err := c.readSecret("CLI token: ")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "aruma_pat_") {
		return errors.New("that is not an Aruma CLI token; create one in the console under Account → CLI tokens")
	}
	creds := credentials{Server: strings.TrimSuffix(server, "/"), Token: token}
	var me struct {
		Account struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	if err := newClient(creds).call("GET", "/me", nil, nil, &me); err != nil {
		return fmt.Errorf("the server refused the token: %w", err)
	}
	path, err := saveCredentials(c.env, creds)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "logged in to %s as %s (saved in %s)\n", creds.Server, me.Account.Email, path)
	return nil
}

func (c *cli) logout() error {
	p, err := credentialsPath(c.env)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(c.stdout, "logged out")
	return nil
}

func (c *cli) readSecret(prompt string) (string, error) {
	if f, ok := c.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(c.stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(c.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// print writes v as JSON with --json, otherwise as a table of the given columns.
func (c *cli) print(v any, rows [][]string, header ...string) error {
	if c.json {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return w.Flush()
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
