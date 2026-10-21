// Command genconfig produces real configuration files for the smoke tests: it starts Aruma
// in-process on a throwaway database, creates a project with one app per platform and a
// service account key through the HTTP APIs, as a developer would, and saves every file next
// to an expected.json the platform tests compare against.
//
//	go run ./test/smoke/genconfig -out test/smoke/.generated
//
// With -serve the server keeps running (for the firebase CLI experiments and the web Auth
// end-to-end tests) and its URL and a CLI token are printed as shell exports; Ctrl-C stops it
// and drops the database. -anonymous turns on anonymous sign-in for the project. -addr picks the
// listen address (a device test maps that port into an emulator) and -db-prefix the name of the
// throwaway database.
//
// -rtdb serves Realtime Database, reached on the main host with ?ns= (the development form of
// ADR-018). -rtdb-domain <domain> implies it and sets rtdb.domain, so the configs carry
// https://smoke-app-default-rtdb.<domain>, and trusts 127.0.0.1 as a proxy. With -serve,
// -rtdb-tls <addr> also puts a TLS front on addr, as Caddy would stand before Aruma (tlsfront.go):
// it serves https and wss for <ns>.<domain> with a certificate from a CA made for the run, writes
// that CA to rtdb-ca.pem in -out, and prints its address as ARUMA_RTDB_TLS_ADDR. Clients then
// resolve the database host to that address themselves.
//
// -switch <addr> serves an off switch for the server on addr, for the offline scenarios of the
// Realtime Database end-to-end tests (switch.go): POST /down closes the listening socket and every
// open connection, POST /up listens again. With -serve its URL is printed as ARUMA_SWITCH_URL.
//
// -log <level> writes the server's own log to stderr from that level up; without it the log is
// dropped.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// Identifiers the platform projects are built around.
const (
	projectID   = "smoke-app"
	packageName = "com.aruma.smoke"
	bundleID    = "com.aruma.smoke"
)

// Expected is written to expected.json.
type Expected struct {
	ServerURL     string `json:"serverUrl"`
	ProjectID     string `json:"projectId"`
	ProjectNumber string `json:"projectNumber"`
	StorageBucket string `json:"storageBucket"`
	AuthDomain    string `json:"authDomain"`
	DatabaseURL   string `json:"databaseURL"`
	Web           AppIDs `json:"web"`
	Android       AppIDs `json:"android"`
	IOS           AppIDs `json:"ios"`
	ServiceEmail  string `json:"serviceAccountEmail"`
	TokenURI      string `json:"tokenUri"`
}

// AppIDs are one app's identifiers.
type AppIDs struct {
	AppID     string `json:"appId"`
	APIKey    string `json:"apiKey"`
	Namespace string `json:"namespace,omitempty"`
}

func main() {
	out := flag.String("out", "test/smoke/.generated", "directory to write the files to")
	serve := flag.Bool("serve", false, "keep the server running after generating")
	anonymous := flag.Bool("anonymous", false, "enable anonymous sign-in for the project")
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on (port 0: any free port)")
	dbPrefix := flag.String("db-prefix", "aruma_smoke_", "prefix of the throwaway database's name")
	var r rtdbOptions
	flag.BoolVar(&r.enabled, "rtdb", false, "serve Realtime Database")
	flag.StringVar(&r.domain, "rtdb-domain", "", "rtdb.domain, e.g. rtdb.aruma.test (implies -rtdb)")
	flag.StringVar(&r.tlsAddr, "rtdb-tls", "", "with -serve and -rtdb-domain: address of a TLS front for the database hosts")
	switchAddr := flag.String("switch", "", "with -serve: address of the server's off switch (POST /down, POST /up)")
	logLevel := flag.String("log", "", "the server's log on stderr from this level up: debug, info, warn or error (default: none)")
	flag.Parse()
	if r.domain != "" {
		r.enabled = true
	}
	if r.tlsAddr != "" && (r.domain == "" || !*serve) {
		log.Fatal("-rtdb-tls needs -rtdb-domain and -serve")
	}
	if *switchAddr != "" && !*serve {
		log.Fatal("-switch needs -serve")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *logLevel != "" {
		var level slog.Level
		if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
			log.Fatalf("-log %q: %v", *logLevel, err)
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}
	if err := run(*out, *addr, *dbPrefix, *switchAddr, *serve, *anonymous, r, logger); err != nil {
		log.Fatal(err)
	}
}

// rtdbOptions are the Realtime Database flags.
type rtdbOptions struct {
	enabled         bool
	domain, tlsAddr string
}

func run(out, addr, dbPrefix, switchAddr string, serve, anonymous bool, rtdb rtdbOptions, logger *slog.Logger) error {
	ctx := context.Background()
	// The name goes into CREATE DATABASE unquoted.
	if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(dbPrefix) {
		return fmt.Errorf("-db-prefix %q: use lower-case letters, digits and _", dbPrefix)
	}
	dbName := dbPrefix + randomHex(4)
	admin, err := pgx.Connect(ctx, storetest.AdminURL())
	if err != nil {
		return fmt.Errorf("connect to Postgres (ARUMA_TEST_DATABASE_URL): %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		return err
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		admin.Close(context.Background())
	}()
	u, _ := url.Parse(storetest.AdminURL())
	u.Path = "/" + dbName
	dbURL := u.String()
	if err := store.Migrate(ctx, dbURL); err != nil {
		return err
	}
	pool, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var ln net.Listener
	var sw *switchListener
	if switchAddr != "" {
		sw, err = newSwitchListener(addr)
		ln = sw
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	vars := map[string]string{"ARUMA_DATABASE_URL": dbURL, "ARUMA_PUBLIC_URL": base, "ARUMA_MASTER_KEY": randomBase64Key()}
	if rtdb.enabled {
		vars["ARUMA_RTDB_ENABLED"] = "true"
	}
	if rtdb.domain != "" {
		// The TLS front, like Caddy, connects from 127.0.0.1 and sets X-Forwarded-*.
		vars["ARUMA_RTDB_DOMAIN"] = rtdb.domain
		vars["ARUMA_TRUSTED_PROXIES"] = "127.0.0.1/32"
	}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		return err
	}
	a, err := app.New(cfg, logger, "smoke", pool)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.Server.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	acc, err := a.Service.CreateAccount(ctx, core.NewAccount{Email: "smoke@example.com", Password: "smoke-test-password"})
	if err != nil {
		return err
	}
	tok, err := a.Service.CreateAccessToken(ctx, &authn.Principal{Kind: authn.KindAccount, AccountID: acc.ID}, "smoke", 0)
	if err != nil {
		return err
	}
	c := &client{base: base + "/console/v1", token: tok.Plaintext}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var proj struct {
		ProjectNumber string `json:"projectNumber"`
		StorageBucket string `json:"storageBucket"`
		AuthDomain    string `json:"authDomain"`
	}
	if err := c.json("POST", "/projects", map[string]string{"projectId": projectID, "displayName": "Smoke"}, &proj); err != nil {
		return err
	}
	exp := Expected{ServerURL: base, ProjectID: projectID, ProjectNumber: proj.ProjectNumber, StorageBucket: proj.StorageBucket, AuthDomain: proj.AuthDomain}
	if anonymous {
		// New projects start without anonymous sign-in, as in Firebase. The console's Sign-in
		// methods screen will switch it; until then the setting is written directly.
		if _, err := pool.Exec(ctx, `INSERT INTO auth.project_config (project_number, anonymous_enabled) VALUES ($1, true)
			ON CONFLICT (project_number) DO UPDATE SET anonymous_enabled = true`, proj.ProjectNumber); err != nil {
			return err
		}
	}

	type appJSON struct {
		AppID    string `json:"appId"`
		APIKeyID string `json:"apiKeyId"`
	}
	create := func(platform, field, ns string) (AppIDs, error) {
		var x appJSON
		body := map[string]string{"platform": platform, "displayName": "Smoke " + platform}
		if field != "" {
			body[field] = ns
		}
		if err := c.json("POST", "/projects/"+projectID+"/apps", body, &x); err != nil {
			return AppIDs{}, err
		}
		var key struct {
			KeyString string `json:"keyString"`
		}
		if err := c.json("GET", "/projects/"+projectID+"/apiKeys/"+x.APIKeyID, nil, &key); err != nil {
			return AppIDs{}, err
		}
		return AppIDs{AppID: x.AppID, APIKey: key.KeyString, Namespace: ns}, nil
	}
	if exp.Web, err = create("WEB", "", ""); err != nil {
		return err
	}
	if exp.Android, err = create("ANDROID", "packageName", packageName); err != nil {
		return err
	}
	if exp.IOS, err = create("IOS", "bundleId", bundleID); err != nil {
		return err
	}

	files := map[string]string{
		"/projects/" + projectID + "/apps/" + exp.Web.AppID + "/config":                                                               "firebase-config.json",
		"/projects/" + projectID + "/apps/" + exp.Android.AppID + "/config":                                                           "google-services.json",
		"/projects/" + projectID + "/apps/" + exp.IOS.AppID + "/config":                                                               "GoogleService-Info.plist",
		"/projects/" + projectID + "/flutterOptions?web=" + exp.Web.AppID + "&android=" + exp.Android.AppID + "&ios=" + exp.IOS.AppID: "firebase_options.dart",
	}
	for path, name := range files {
		b, err := c.raw("GET", path, nil)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
			return err
		}
	}
	var web struct {
		DatabaseURL string `json:"databaseURL"`
	}
	b, _ := os.ReadFile(filepath.Join(out, "firebase-config.json"))
	_ = json.Unmarshal(b, &web)
	exp.DatabaseURL = web.DatabaseURL

	var sa struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := c.json("POST", "/projects/"+projectID+"/serviceAccounts", map[string]string{"accountId": "smoke-backend"}, &sa); err != nil {
		return err
	}
	key, err := c.raw("POST", "/projects/"+projectID+"/serviceAccounts/"+sa.ID+"/keys", nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "service-account.json"), key, 0o600); err != nil {
		return err
	}
	exp.ServiceEmail = sa.Email
	exp.TokenURI = base + "/oauth2/v1/token"

	eb, _ := json.MarshalIndent(exp, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "expected.json"), append(eb, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote config files to %s\n", out)

	if serve {
		fmt.Printf("export ARUMA_URL=%s\n", base)
		if rtdb.tlsAddr != "" {
			front, err := startTLSFront(rtdb.tlsAddr, rtdb.domain, base, filepath.Join(out, "rtdb-ca.pem"))
			if err != nil {
				return err
			}
			defer front.Close()
			fmt.Printf("export ARUMA_RTDB_TLS_ADDR=%s\n", front.Addr())
		}
		if sw != nil {
			u, stop, err := serveSwitch(switchAddr, sw)
			if err != nil {
				return err
			}
			defer stop()
			fmt.Printf("export ARUMA_SWITCH_URL=%s\n", u)
		}
		// Readers wait for this line: it comes last.
		fmt.Printf("export ARUMA_CLI_TOKEN=%s\n", tok.Plaintext)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
	}
	return nil
}

type client struct{ base, token string }

func (c *client) raw(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, errors.New(method + " " + path + ": " + string(b))
	}
	return b, nil
}

func (c *client) json(method, path string, body, out any) error {
	b, err := c.raw(method, path, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomBase64Key() string {
	k := crypto.RandomBytes(32)
	return encodeStd(k)
}

func encodeStd(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
