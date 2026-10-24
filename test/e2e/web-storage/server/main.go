// Command server is the Aruma side of the web Storage end-to-end tests (ADR-022 §16.1): it starts
// Aruma in-process with Storage on a throwaway database, creates a project with anonymous sign-in
// and a web app through the console API as a developer would, deploys the Storage rules below, gives
// the bucket Firebase's documented cors.json (the SDK's getBytes and getBlob, and a fetch of a link,
// cross origins) and stores public/ssr.png for the SSR page. e2e.json has the server's URL, the
// web config the console serves, and the bucket.
//
//	go run ./test/e2e/web-storage/server -addr 127.0.0.1:9420 -out test/e2e/web-storage/.generated
//
// It prints "ready" on stdout once e2e.json is written, and serves until SIGTERM or SIGINT, then
// drops the database.
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
	"image"
	"image/color"
	"image/png"
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
	"github.com/arumadev/aruma/internal/platform/rules"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

const projectID = "web-storage"

// storageRules are the rules the tests run under: a signed-in user owns e2e/<uid>/; public/ is
// readable by anyone (the SSR page reads it with no user); open/ is open to anyone, for the clients
// that sign no one in (Flutter web, which has no Auth redirect here).
const storageRules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /e2e/{uid}/{all=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
    match /public/{all=**} {
      allow read: if true;
    }
    match /open/{all=**} {
      allow read, write: if true;
    }
  }
}
`

// documentedCORS is the cors.json of Firebase's download-files page, as gsutil cors set sets it.
var documentedCORS = []storage.CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 3600}}

// E2E is what the tests read.
type E2E struct {
	Aruma  string          `json:"aruma"`
	Config json.RawMessage `json:"config"` // the web config the console serves
	Bucket string          `json:"bucket"`
	SSR    string          `json:"ssr"` // public/ssr.png's name
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9420", "address to listen on")
	dbPrefix := flag.String("db-prefix", "aruma_e2e_", "prefix of the throwaway database's name")
	out := flag.String("out", "test/e2e/web-storage/.generated", "directory of e2e.json and the data directory")
	flag.Parse()
	if err := run(*addr, *dbPrefix, *out); err != nil {
		log.Fatal(err)
	}
}

func run(addr, dbPrefix, out string) error {
	ctx := context.Background()
	// The name goes into CREATE DATABASE unquoted.
	if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(dbPrefix) {
		return fmt.Errorf("-db-prefix %q: use lower-case letters, digits and _", dbPrefix)
	}
	dataDir, err := filepath.Abs(filepath.Join(out, "data"))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
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

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	vars := map[string]string{
		"ARUMA_DATABASE_URL":     dbURL,
		"ARUMA_PUBLIC_URL":       base,
		"ARUMA_MASTER_KEY":       base64.StdEncoding.EncodeToString(key),
		"ARUMA_STORAGE_ENABLED":  "true",
		"ARUMA_STORAGE_DATA_DIR": dataDir,
	}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		return err
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), "web-storage", pool)
	if err != nil {
		return err
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	if err := a.Storage.Boot(runCtx); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.Storage.Run(runCtx); err != nil && runCtx.Err() == nil {
			log.Printf("storage: %v", err)
		}
	}()
	defer func() {
		stop()
		<-done
		_ = a.Storage.Close()
	}()
	srv := &http.Server{Handler: a.Server.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	acc, err := a.Service.CreateAccount(ctx, tcore.NewAccount{Email: "web-storage@example.com", Password: "web-storage-password"})
	if err != nil {
		return err
	}
	owner := &authn.Principal{Kind: authn.KindAccount, AccountID: acc.ID}
	tok, err := a.Service.CreateAccessToken(ctx, owner, "web-storage", 0)
	if err != nil {
		return err
	}
	c := &client{base: base + "/console/v1", token: tok.Plaintext}

	var proj struct {
		ProjectNumber string `json:"projectNumber"`
		StorageBucket string `json:"storageBucket"`
	}
	if err := c.json("POST", "/projects", map[string]string{"projectId": projectID, "displayName": "Web Storage"}, &proj); err != nil {
		return err
	}
	// New projects start without anonymous sign-in, as in Firebase; genconfig turns it on the same way.
	if _, err := pool.Exec(ctx, `INSERT INTO auth.project_config (project_number, anonymous_enabled) VALUES ($1, true)
		ON CONFLICT (project_number) DO UPDATE SET anonymous_enabled = true`, proj.ProjectNumber); err != nil {
		return err
	}
	var web struct {
		AppID string `json:"appId"`
	}
	if err := c.json("POST", "/projects/"+projectID+"/apps", map[string]string{"platform": "WEB", "displayName": "Web Storage"}, &web); err != nil {
		return err
	}
	webConfig, err := c.raw("GET", "/projects/"+projectID+"/apps/"+web.AppID+"/config", nil)
	if err != nil {
		return err
	}

	rs, _, err := a.Service.CreateRuleset(ctx, owner, projectID, rules.Storage, []rules.File{{Name: "storage.rules", Content: storageRules}})
	if err != nil {
		return err
	}
	if _, err := a.Service.PublishRelease(ctx, owner, projectID, "firebase.storage/"+proj.StorageBucket, rs.Name); err != nil {
		return err
	}
	b, err := a.Storage.Bucket(ctx, proj.StorageBucket)
	if err != nil {
		return err
	}
	asAdmin := storage.Principal{Kind: storage.PrincipalAdmin}
	if _, err := a.Storage.SetCORS(ctx, b, asAdmin, documentedCORS); err != nil {
		return err
	}
	const ssrName = "public/ssr.png"
	pixel := pngBytes()
	spec := storage.UploadSpec{Name: ssrName, Declared: int64(len(pixel)), Surface: storage.SurfaceFirebase,
		Attrs: storage.Patch{Fields: map[string]*string{storage.FieldContentType: new("image/png")}}}
	if _, err := a.Storage.Insert(ctx, b, asAdmin, spec, bytes.NewReader(pixel), int64(len(pixel))); err != nil {
		return fmt.Errorf("insert %s: %w", ssrName, err)
	}

	e := E2E{Aruma: base, Config: webConfig, Bucket: b.Name, SSR: ssrName}
	eb, _ := json.MarshalIndent(e, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "e2e.json"), append(eb, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
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

// pngBytes is a 4×4 green PNG.
func pngBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			img.Set(x, y, color.RGBA{G: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
