package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"time"
)

// list returns the names of the objects under prefix.
func (r *run) list(ctx context.Context, prefix string) (map[string]bool, error) {
	out := map[string]bool{}
	token := ""
	for {
		u := r.base + "?prefix=" + url.QueryEscape(prefix) + "&maxResults=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := r.hc.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			return nil, fmt.Errorf("list: %d %v", resp.StatusCode, err)
		}
		for _, it := range page.Items {
			out[it.Name] = true
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		token = page.NextPageToken
	}
}

func (r *run) check(ctx context.Context, clients []*client, rep *report) {
	listed, err := r.list(ctx, "o/")
	if err != nil {
		r.Violation("list the objects: %v", err)
		return
	}
	rep.Objects = len(listed)
	known := map[string]bool{}
	for _, c := range clients {
		for _, name := range c.names {
			known[name] = true
			want := c.want[name]
			if listed[name] != want.exists {
				r.Violation("%s: listed=%v, the client knows %s", name, listed[name], want)
			}
			got, err := c.fetch(ctx, name)
			if err != nil {
				r.Violation("%s: read back: %v", name, err)
				continue
			}
			if got != want {
				r.Violation("%s: holds %s, the client knows %s", name, got, want)
			}
		}
	}
	for name := range listed {
		if !known[name] {
			r.Violation("%s is listed and no client wrote it", name)
		}
	}
	r.checkDisk(ctx, rep)
}

var keyFile = regexp.MustCompile(`^([ps])/([0-9]+)/[0-9a-f]{2}/([0-9a-f-]{36})$`)

// diskScan is the data directory against the rows.
type diskScan struct {
	counts map[string]int
	bad    []string // object rows without their bytes
	other  []string // files that are not blobs
	used   int64    // the bytes of the object rows
	nobj   int
}

func (d diskScan) clean() bool {
	return d.counts["orphan file"] == 0 && d.counts["file held by gc"] == 0 && d.counts["file held by session"] == 0 && d.counts["blob_gc rows"] == 0
}

// scan reads the rows and walks the data directory.
func (r *run) scan(ctx context.Context, conn *pgx.Conn) (diskScan, error) {
	objects := map[string]int64{}
	rows, err := conn.Query(ctx, `SELECT blob_key, size FROM storage.objects`)
	if err != nil {
		return diskScan{}, err
	}
	var d diskScan
	for rows.Next() {
		var k string
		var n int64
		_ = rows.Scan(&k, &n)
		objects[k] = n
		d.used += n
	}
	rows.Close()
	held := map[string]string{}
	for _, q := range []struct{ what, sql string }{
		{"session", `SELECT staging_key FROM storage.upload_sessions WHERE staging_key IS NOT NULL UNION ALL SELECT blob_key FROM storage.upload_sessions WHERE blob_key IS NOT NULL`},
		{"gc", `SELECT blob_key FROM storage.blob_gc`},
	} {
		rows, err := conn.Query(ctx, q.sql)
		if err != nil {
			return diskScan{}, err
		}
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			held[k] = q.what
		}
		rows.Close()
	}
	files := map[string]int64{}
	_ = filepath.WalkDir(r.dataDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(r.dataDir, p)
		rel = filepath.ToSlash(rel)
		if m := keyFile.FindStringSubmatch(rel); m != nil {
			if fi, err := e.Info(); err == nil {
				files[m[1]+"/"+m[2]+"/"+m[3]] = fi.Size()
			}
			return nil
		}
		d.other = append(d.other, rel)
		return nil
	})
	d.nobj = len(objects)
	d.counts = map[string]int{"objects": len(objects), "other files": len(d.other)}
	for k, n := range objects {
		sz, ok := files[k]
		switch {
		case !ok:
			d.counts["row without file"]++
			d.bad = append(d.bad, fmt.Sprintf("object row %s has no file", k))
		case sz != n:
			d.counts["file of the wrong size"]++
			d.bad = append(d.bad, fmt.Sprintf("object row %s: size %d, its file holds %d bytes", k, n, sz))
		}
	}
	for k := range files {
		if _, ok := objects[k]; ok {
			continue
		}
		if what, ok := held[k]; ok {
			d.counts["file held by "+what]++
		} else {
			d.counts["orphan file"]++
		}
	}
	var sessions, gcRows int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM storage.upload_sessions`).Scan(&sessions)
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM storage.blob_gc`).Scan(&gcRows)
	d.counts["sessions"], d.counts["blob_gc rows"] = sessions, gcRows
	return d, nil
}

// whileDown scans the data directory while the server is down after a SIGKILL: every object
// row must have its whole file, and the orphans the kill left are counted, for the sweep at the
// next boot to delete.
func (r *run) whileDown(ctx context.Context, how string) {
	conn, err := r.Srv.DB(ctx)
	if err != nil {
		r.Violation("connect to the database: %v", err)
		return
	}
	defer conn.Close(ctx)
	d, err := r.scan(ctx, conn)
	if err != nil {
		r.Violation("scan while down: %v", err)
		return
	}
	for i, b := range d.bad {
		if i == 5 {
			break
		}
		r.Violation("after %s, before the restart: %s", how, b)
	}
	r.downMu.Lock()
	r.orphansSeen += d.counts["orphan file"]
	r.heldSeen += d.counts["file held by session"] + d.counts["file held by gc"]
	r.downMu.Unlock()
}

// checkDisk compares the data directory with the rows, until the sweep, the GC and the session
// expiry have done their work or the settle time ran out.
func (r *run) checkDisk(ctx context.Context, rep *report) {
	conn, err := r.Srv.DB(ctx)
	if err != nil {
		r.Violation("connect to the database: %v", err)
		return
	}
	defer conn.Close(ctx)
	start := time.Now()
	for first := true; ; first = false {
		d, err := r.scan(ctx, conn)
		if err != nil {
			r.Violation("scan: %v", err)
			return
		}
		if first {
			rep.Cleanup["at the end of the writes"] = d.counts
		}
		if d.clean() || time.Since(start) > r.cfg.settle {
			var usedRow, countRow int64
			if err := conn.QueryRow(ctx, `SELECT u.used_bytes, u.object_count FROM storage.usage u`).Scan(&usedRow, &countRow); err == nil {
				if usedRow != d.used || countRow != int64(d.nobj) {
					r.Violation("storage.usage says %d bytes in %d objects; the rows hold %d bytes in %d", usedRow, countRow, d.used, d.nobj)
				}
			}
			if !d.clean() {
				r.Violation("after %v the data directory still holds %v; other files %v", r.cfg.settle, d.counts, d.other)
			}
			for i, b := range d.bad {
				if i == 5 {
					r.Violation("and %d more object rows without their bytes", len(d.bad)-5)
					break
				}
				r.Violation("%s", b)
			}
			rep.Cleanup["settled in"] = time.Since(start).Round(time.Second).String()
			rep.Cleanup["at the end"] = d.counts
			r.downMu.Lock()
			rep.Cleanup["orphans seen while down (summed over the kills)"] = r.orphansSeen
			rep.Cleanup["files held by sessions or gc while down (summed)"] = r.heldSeen
			r.downMu.Unlock()
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}
