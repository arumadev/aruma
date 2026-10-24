// Command server is the Aruma side of the browser matrix (ADR-022 §10.6, F §2.4): it starts
// Aruma in-process with Storage on a throwaway database and creates two projects whose Storage
// rules open public/ to reads. It stores the matrix's objects through the /v0 surface (each with
// its download token and the inline disposition an SDK upload leaves) in the first bucket, which
// gets Firebase's documented cors.json, and the objects fetch and the SDK read also in the
// second, which keeps no CORS. manifest.json has the server's URL and each object's download URL.
//
//	go run ./test/e2e/browser-matrix/server -addr 127.0.0.1:9260 \
//	  -fixtures test/e2e/browser-matrix/fixtures -out test/e2e/browser-matrix/.generated
//
// It prints "ready" on stdout once the manifest is written, and serves until SIGTERM or
// SIGINT, then drops the database.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"strings"
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

// marker is in every page a script would run in: the tests look for it on the console.
const marker = "MATRIX-SCRIPT-RAN"

// evilHTML is a page that, if its script ran, would say so on the console, retitle itself and
// reach for the origin's cookies and storage.
const evilHTML = `<!doctype html><html><head><title>matrix</title></head><body><p id="p">evil page</p>
<script>console.log("` + marker + `"); document.title = "executed";
try { localStorage.setItem("matrix", document.cookie); } catch (e) {}</script></body></html>`

const evilSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="30" viewBox="0 0 40 30">
<rect id="r" width="40" height="30" fill="green"/>
<script>console.log("` + marker + `"); document.getElementById("r").setAttribute("fill", "red");</script></svg>`

// swJS would be a service worker that answers every download in its scope.
const swJS = `console.log("` + marker + `");
self.addEventListener("fetch", (e) => e.respondWith(new Response("intercepted")));`

// minimalPDF is a one-page PDF with no text: enough for a viewer to open.
const minimalPDF = `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >> endobj
trailer << /Root 1 0 R >>
%%EOF
`

// object is one object of the matrix.
type object struct {
	key, name, body string
	fields          map[string]string
}

// Entry is an object in the manifest.
type Entry struct {
	Name        string `json:"name"`
	URL         string `json:"url"` // the download URL getDownloadURL gives: alt=media&token=
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
	Body        string `json:"body,omitempty"` // the text objects' bytes, decoded
}

// Manifest is what the tests read.
type Manifest struct {
	Aruma        string           `json:"aruma"`
	Marker       string           `json:"marker"`
	Bucket       string           `json:"bucket"` // with Firebase's documented cors.json
	Objects      map[string]Entry `json:"objects"`
	NoCORSBucket string           `json:"noCORSBucket"` // with no CORS, as a new bucket
	NoCORS       map[string]Entry `json:"noCORS"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9260", "address to listen on")
	dbPrefix := flag.String("db-prefix", "aruma_e2e_", "prefix of the throwaway database's name")
	fixtures := flag.String("fixtures", "test/e2e/browser-matrix/fixtures", "directory of the media fixtures")
	out := flag.String("out", "test/e2e/browser-matrix/.generated", "directory of manifest.json and the data directory")
	flag.Parse()
	if err := run(*addr, *dbPrefix, *fixtures, *out); err != nil {
		log.Fatal(err)
	}
}

func run(addr, dbPrefix, fixtures, out string) error {
	ctx := context.Background()
	// The name goes into CREATE DATABASE unquoted.
	if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(dbPrefix) {
		return fmt.Errorf("-db-prefix %q: use lower-case letters, digits and _", dbPrefix)
	}
	objects, err := matrixObjects(fixtures)
	if err != nil {
		return err
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
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), "matrix", pool)
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

	acc, err := a.Service.CreateAccount(ctx, tcore.NewAccount{Email: "matrix@example.com", Password: "matrix-test-password"})
	if err != nil {
		return err
	}
	owner := &authn.Principal{Kind: authn.KindAccount, AccountID: acc.ID}
	m := Manifest{Aruma: base, Marker: marker, Objects: map[string]Entry{}, NoCORS: map[string]Entry{}}
	// The matrix's bucket has Firebase's documented cors.json (download-files, "CORS
	// configuration"): every origin may GET. The other has none, as a new Firebase bucket.
	if m.Bucket, err = seed(ctx, a, owner, base, "matrix-app", objects, m.Objects, documentedCORS); err != nil {
		return err
	}
	if m.NoCORSBucket, err = seed(ctx, a, owner, base, "matrix-nocors", sdkObjects(objects), m.NoCORS, nil); err != nil {
		return err
	}

	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(mb, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("ready")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

// documentedCORS is the cors.json of Firebase's download-files page, as gsutil cors set sets it.
var documentedCORS = []storage.CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 3600}}

// readRules let anyone read public/ by the rules, for the SDK's getBytes and getBlob, which go by
// the rules and not by a token; everything else stays locked.
const readRules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /public/{all=**} { allow read: if true; }
  }
}
`

// seed creates a project with the rules readRules and the CORS rules given, stores objs in its
// bucket through the /v0 surface, and records each in entries. It returns the bucket's name.
func seed(ctx context.Context, a *app.App, owner *authn.Principal, base, id string, objs []object,
	entries map[string]Entry, cors []storage.CORSRule) (string, error) {
	p, err := a.Service.CreateProject(ctx, owner, tcore.NewProject{ID: id, DisplayName: "Browser matrix"})
	if err != nil {
		return "", err
	}
	rs, _, err := a.Service.CreateRuleset(ctx, owner, id, rules.Storage, []rules.File{{Name: "storage.rules", Content: readRules}})
	if err != nil {
		return "", err
	}
	if _, err := a.Service.PublishRelease(ctx, owner, id, "firebase.storage/"+p.StorageBucket, rs.Name); err != nil {
		return "", err
	}
	b, err := a.Storage.Bucket(ctx, p.StorageBucket)
	if err != nil {
		return "", err
	}
	asAdmin := storage.Principal{Kind: storage.PrincipalAdmin}
	for _, o := range objs {
		spec := storage.UploadSpec{Name: o.name, Declared: int64(len(o.body)), Surface: storage.SurfaceFirebase,
			Attrs: storage.Patch{Fields: map[string]*string{}}}
		for k, v := range o.fields {
			spec.Attrs.Fields[k] = new(v)
		}
		stored, err := a.Storage.Insert(ctx, b, asAdmin, spec, strings.NewReader(o.body), int64(len(o.body)))
		if err != nil {
			return "", fmt.Errorf("insert %s: %w", o.name, err)
		}
		e := Entry{Name: o.name, Size: stored.Size, ContentType: stored.ContentType,
			URL: base + "/v0/b/" + url.PathEscape(b.Name) + "/o/" + url.PathEscape(o.name) + "?alt=media&token=" + stored.DownloadTokens[0]}
		if strings.HasPrefix(o.fields[storage.FieldContentType], "text/") {
			e.Body = o.body
			if o.fields[storage.FieldContentEncoding] == "gzip" {
				e.Body = gzipPlain
			}
		}
		entries[o.key] = e
	}
	if cors != nil {
		if _, err := a.Storage.SetCORS(ctx, b, asAdmin, cors); err != nil {
			return "", err
		}
	}
	return b.Name, nil
}

// sdkObjects are the objects of the bucket without CORS: the ones fetch and the SDK read.
func sdkObjects(objs []object) []object {
	var out []object
	for _, o := range objs {
		if o.key == "data" || o.key == "sdk" {
			out = append(out, o)
		}
	}
	return out
}

// gzipPlain is what gzip.txt decompresses to.
var gzipPlain = strings.Repeat("matrix gzip ", 64)

// matrixObjects are the objects of F §2.4's cases, by the key the tests use.
func matrixObjects(fixtures string) ([]object, error) {
	ct := func(t string) map[string]string { return map[string]string{storage.FieldContentType: t} }
	objs := []object{
		{"evilHTML", "evil.html", evilHTML, ct("text/html")},   // 1
		{"evilSVG", "evil.svg", evilSVG, ct("image/svg+xml")},  // 2
		{"untypedHTML", "x.html", evilHTML, nil},               // 3
		{"plainHTML", "plain.txt", evilHTML, ct("text/plain")}, // 4
		{"inlineHTML", "inline.html", evilHTML, map[string]string{ // 5
			storage.FieldContentType: "text/html", storage.FieldContentDisposition: "inline"}},
		{"frameText", "frame.txt", "MATRIX-FRAME-CONTENT", ct("text/plain")}, // 9
		{"data", "data.txt", "0123456789abcdefghij", ct("text/plain")},       // 7, 10
		{"sw", "sw.js", swJS, ct("text/javascript")},                         // 8
		{"pdf", "report.pdf", minimalPDF, ct("application/pdf")},             // 13
		{"gzip", "gzip.txt", gzipString(gzipPlain), map[string]string{ // 11, as a browser sees it
			storage.FieldContentType: "text/plain", storage.FieldContentEncoding: "gzip"}},
		{"png", "dot.png", pngString(), ct("image/png")},                   // 6
		{"sdk", "public/sdk.txt", "bytes the SDK reads", ct("text/plain")}, // 7, by getBytes and getBlob
	}
	for _, f := range []struct{ key, file, typ string }{
		{"mp4", "clip.mp4", "video/mp4"}, {"webm", "clip.webm", "video/webm"}, {"mp3", "song.mp3", "audio/mpeg"}, // 14
	} {
		b, err := os.ReadFile(filepath.Join(fixtures, f.file))
		if err != nil {
			return nil, fmt.Errorf("the media fixtures (fixtures/make.sh): %w", err)
		}
		objs = append(objs, object{f.key, "media/" + f.file, string(b), ct(f.typ)})
	}
	return objs, nil
}

func gzipString(s string) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = io.WriteString(zw, s)
	_ = zw.Close()
	return buf.String()
}

// pngString is a 20×10 blue PNG.
func pngString() string {
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := range 10 {
		for x := range 20 {
			img.Set(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
