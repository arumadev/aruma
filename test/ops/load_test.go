package ops

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// load is clients writing to a deployment while an operator backs it up.
type load struct {
	t      *testing.T
	d      *deployment
	o      *owner
	p      *project
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	rtdb  map[int]time.Time // acknowledged write -> when
	docs  map[int]time.Time
	files map[string]time.Time
}

func startLoad(t *testing.T, d *deployment, fx *fixture, p *project) *load {
	ctx, cancel := context.WithCancel(context.Background())
	l := &load{t: t, d: d, o: &owner{a: newAPI(t, d.base()), pat: fx.PAT}, p: p, cancel: cancel,
		rtdb: map[int]time.Time{}, docs: map[int]time.Time{}, files: map[string]time.Time{}}
	l.wg.Add(3)
	go func() { defer l.wg.Done(); l.writeRTDB(ctx) }()
	go func() { defer l.wg.Done(); l.writeDocs(ctx) }()
	go func() { defer l.wg.Done(); l.writeFiles(ctx) }()
	return l
}

func (l *load) stop() { l.cancel(); l.wg.Wait() }

func (l *load) writeRTDB(ctx context.Context) {
	for i := 0; ctx.Err() == nil; i++ {
		r, err := l.o.a.try("PUT", rtdbURL(l.p, fmt.Sprintf("load/k%d", i), ""), bearer(l.o.pat), map[string]any{"i": i, "pad": strings.Repeat("x", 200)})
		if err == nil && r.Status == 200 {
			l.mu.Lock()
			l.rtdb[i] = time.Now()
			l.mu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (l *load) writeDocs(ctx context.Context) {
	for i := 0; ctx.Err() == nil; i++ {
		name := fmt.Sprintf("projects/%s/databases/(default)/documents/load/d%d", l.p.ID, i)
		r, err := l.o.a.try("POST", docsBase(l.p)+"/documents:commit", bearer(l.o.pat), map[string]any{"writes": []any{
			map[string]any{"update": map[string]any{"name": name, "fields": map[string]any{"i": map[string]any{"integerValue": fmt.Sprint(i)}}}}}})
		if err == nil && r.Status == 200 {
			l.mu.Lock()
			l.docs[i] = time.Now()
			l.mu.Unlock()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeFiles stores new objects of 50 KiB to 3 MiB, overwrites one name over and over (each
// overwrite leaves the old file for the sweeper), and deletes some.
func (l *load) writeFiles(ctx context.Context) {
	tok := l.p.token(l.t, l.o.a)
	for i := 0; ctx.Err() == nil; i++ {
		name := fmt.Sprintf("load/o%d", i)
		size := 50<<10 + (i%7)*(500<<10)
		if err := l.upload(tok, name, size); err == nil {
			l.mu.Lock()
			l.files[name] = time.Now()
			l.mu.Unlock()
		}
		_ = l.upload(tok, "load/hot", 20<<10+i)
		if i%3 == 0 {
			tmp := fmt.Sprintf("load/tmp%d", i)
			if l.upload(tok, tmp, 10<<10) == nil {
				_, _ = l.o.a.try("DELETE", "/v0/b/"+l.p.Bucket+"/o/"+url.PathEscape(tmp), bearer(tok), nil)
			}
		}
	}
}

func (l *load) upload(tok, name string, size int) error {
	data := det(l.p.ID+"/"+name+fmt.Sprint(size), size)
	h := bearer(tok)
	h["X-Goog-Upload-Protocol"] = "resumable"
	h["X-Goog-Upload-Command"] = "start"
	r, err := l.o.a.try("POST", "/v0/b/"+l.p.Bucket+"/o?uploadType=resumable&name="+url.QueryEscape(name), h, map[string]any{"contentType": "application/octet-stream"})
	if err != nil || r.Status != 200 {
		return fmt.Errorf("start: %v %v", err, r)
	}
	sess := r.Header.Get("X-Goog-Upload-Url")
	step := 1 << 20
	for off := 0; ; off += step {
		end := min(off+step, len(data))
		h := bearer(tok)
		h["X-Goog-Upload-Protocol"] = "resumable"
		h["X-Goog-Upload-Command"] = "upload"
		if end == len(data) {
			h["X-Goog-Upload-Command"] = "upload, finalize"
		}
		h["X-Goog-Upload-Offset"] = fmt.Sprint(off)
		r, err := l.o.a.try("POST", sess, h, data[off:end])
		if err != nil || r.Status != 200 {
			return fmt.Errorf("chunk: %v %v", err, r)
		}
		if end == len(data) {
			return nil
		}
	}
}

var fileKey = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// storageConsistency compares the Storage rows of the deployment's database with the files in
// its data directory: no object or live upload without its file, and, once the server has
// swept after its start, no file without a row. It returns what it found wrong.
func storageConsistency(t *testing.T, d *deployment, wantNoOrphans bool) (rowsWithoutFile, filesWithoutRow []string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, d.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	disk := map[string]bool{}
	for _, top := range []string{"p", "s"} {
		_ = filepath.WalkDir(filepath.Join(d.dir, top), func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !fileKey.MatchString(e.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(d.dir, path)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			disk[parts[0]+"/"+parts[1]+"/"+e.Name()] = true
			return nil
		})
	}
	rows := map[string]string{} // key -> what it is
	for _, q := range []struct{ what, sql string }{
		{"object", `SELECT blob_key FROM storage.objects`},
		{"upload", `SELECT blob_key FROM storage.upload_sessions WHERE blob_key IS NOT NULL AND state = 'active'`},
		{"staging", `SELECT staging_key FROM storage.upload_sessions WHERE state = 'active'`},
		{"blob_gc", `SELECT blob_key FROM storage.blob_gc`},
	} {
		rs, err := conn.Query(ctx, q.sql)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var k string
			if err := rs.Scan(&k); err != nil {
				t.Fatal(err)
			}
			rows[k] = q.what
		}
		rs.Close()
	}
	for k, what := range rows {
		if !disk[k] && what != "blob_gc" && what != "upload" { // an upload's object key is reserved before any file
			rowsWithoutFile = append(rowsWithoutFile, what+" "+k)
		}
	}
	for k := range disk {
		if _, ok := rows[k]; !ok {
			filesWithoutRow = append(filesWithoutRow, k)
		}
	}
	return
}

// checkObjectBytes downloads every object of the project and compares the bytes with the
// metadata the server holds for them: a row whose file is missing or damaged fails here.
func checkObjectBytes(t *testing.T, a *api, p *project) (checked int) {
	t.Helper()
	tok := p.token(t, a)
	r := a.must("GET", "/storage/v1/b/"+p.Bucket+"/o?maxResults=1000", bearer(tok), nil)
	items, _ := r.obj()["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		name := fmt.Sprint(m["name"])
		dl := a.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(name)+"?alt=media", bearer(tok), nil)
		sum := md5.Sum(dl.Body)
		if dl.Status != 200 || base64.StdEncoding.EncodeToString(sum[:]) != m["md5Hash"] || fmt.Sprint(len(dl.Body)) != m["size"] {
			t.Errorf("%s: object %q: download %d, %d bytes (row: size %v md5 %v)", p.ID, name, dl.Status, len(dl.Body), m["size"], m["md5Hash"])
		}
		checked++
	}
	return checked
}
