package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The tests on a real server, as the surfaces will call Serve: on S2-F's ports (9040-9049),
// with the server's own 60 s WriteTimeout. The handlers return normally whatever Serve did,
// as a careless surface would: the cut is Serve's own.

func listen(t *testing.T) net.Listener {
	t.Helper()
	for port := 9040; port <= 9049; port++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return l
		}
	}
	t.Fatal("no free port in 9040-9049")
	return nil
}

// server serves it from open() in mode m, as a surface does, and sends Serve's error on errs.
func server(t *testing.T, it Item, open func() io.ReadSeeker, m Mode, lim Limits) (*httptest.Server, <-chan error) {
	t.Helper()
	errs := make(chan error, 16)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() { errs <- err }() // runs on Serve's abort too
		l := lim
		l.Cut = func(e error) { err = e }
		err = Serve(w, r, it, open(), m, l)
	}))
	srv.Listener.Close()
	srv.Listener = listen(t)
	srv.Config.WriteTimeout = 60 * time.Second // server.go
	srv.Config.ErrorLog = nil
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, errs
}

// goCRC32c is cloud.google.com/go/storage v1.62.1's parseCRC32c (reader.go:297-313): it reads
// res.Header["X-Goog-Hash"], splits each value on "," and takes the first crc32c=.
func goCRC32c(res *http.Response) (uint32, bool) {
	const prefix = "crc32c="
	for _, spec := range res.Header["X-Goog-Hash"] {
		for _, v := range strings.Split(spec, ",") {
			if strings.HasPrefix(v, prefix) {
				b, err := base64.StdEncoding.DecodeString(v[len(prefix):])
				if err == nil && len(b) == 4 {
					return binary.BigEndian.Uint32(b), true
				}
			}
		}
	}
	return 0, false
}

// nodeHashes is @google-cloud/storage's split (file.js:1104-1111): the header as one string,
// split on "," with no trimming, each pair cut at its first "=". Node joins repeated headers
// with ", ", so a second header would become the key " md5".
func nodeHashes(res *http.Response) map[string]string {
	hashes := map[string]string{}
	for pair := range strings.SplitSeq(strings.Join(res.Header.Values("X-Goog-Hash"), ", "), ",") {
		k, v, _ := strings.Cut(pair, "=")
		hashes[k] = v
	}
	return hashes
}

// TestHashesAsGoAndNodeParseThem: over the wire, the checksums reach both Admin SDKs, and each
// one's validation condition holds (Go: not 206, not decompressed; Node: stored identity).
func TestHashesAsGoAndNodeParseThem(t *testing.T) {
	data := bytes.Repeat([]byte("aruma storage "), 5000)
	it := object("a.bin", "application/octet-stream", data)
	srv, _ := server(t, it, func() io.ReadSeeker { return bytes.NewReader(data) }, GCS, generous)

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(body, data) {
		t.Fatalf("body: %d bytes", len(body))
	}
	if n := len(res.Header["X-Goog-Hash"]); n != 1 {
		t.Errorf("%d x-goog-hash headers", n)
	}
	crc, ok := goCRC32c(res)
	if !ok || crc != crc32.Checksum(body, castagnoli) {
		t.Errorf("Go: crc32c %v %08x", ok, crc)
	}
	if res.Uncompressed || res.Header.Get("X-Goog-Stored-Content-Encoding") != "identity" {
		t.Errorf("Go would not check: uncompressed %v, stored %q", res.Uncompressed, res.Header.Get("X-Goog-Stored-Content-Encoding"))
	}
	h := nodeHashes(res)
	if h["crc32c"] != base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, it.CRC32C)) ||
		h["md5"] != base64.StdEncoding.EncodeToString(it.MD5[:]) || len(h) != 2 {
		t.Errorf("Node: %v", h)
	}
	if res.Header.Get("X-Goog-Metageneration") != "3" || res.Header.Get("X-Goog-Generation") != "1784647629568292" {
		t.Errorf("generations: %v", res.Header)
	}
	if _, err := http.ParseTime(res.Header.Get("Last-Modified")); err != nil {
		t.Errorf("Go fails the read on Last-Modified: %v", err)
	}

	// Go's ranged read: 206 with a Content-Range it parses (http_client.go:1557-1580).
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=100-")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	cr := res.Header.Get("Content-Range")
	if res.StatusCode != 206 || cr != fmt.Sprintf("bytes 100-%d/%d", len(data)-1, len(data)) ||
		!bytes.Equal(body, data[100:]) || cr[len("bytes="):strings.Index(cr, "-")] != "100" {
		t.Errorf("range: %d %q", res.StatusCode, cr)
	}
}

// TestGzipThroughGoClient: Go's transport asks for gzip on a full read and decompresses itself
// (B §6.3); a client that does not gets the server's decompression, and Go marks it
// uncompressedByServer, so neither checks the stored bytes' hash against what it read.
func TestGzipThroughGoClient(t *testing.T) {
	plain := []byte(strings.Repeat("compressible ", 10000))
	it, stored := gzipObject(t, plain)
	srv, _ := server(t, it, func() io.ReadSeeker { return bytes.NewReader(stored) }, GCS, generous)

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !res.Uncompressed || !bytes.Equal(body, plain) {
		t.Errorf("transport gzip: uncompressed %v, %d bytes", res.Uncompressed, len(body))
	}

	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()
	res, err = (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	byServer := res.Header.Get("X-Goog-Stored-Content-Encoding") == "gzip" && res.Header.Get("Content-Encoding") != "gzip"
	if !byServer || !bytes.Equal(body, plain) || res.ContentLength != -1 {
		t.Errorf("server gzip: by server %v, length %d, %d bytes", byServer, res.ContentLength, len(body))
	}
}

// TestCutIsSeenByTheClient: past the decompression cap the client gets a broken body, never a
// short one that ends like a whole.
func TestCutIsSeenByTheClient(t *testing.T) {
	stored := bomb(t, 4) // 64 MiB of zeros
	it := object("bomb", "text/plain", stored)
	it.ContentEncoding = "gzip"
	srv, errs := server(t, it, func() io.ReadSeeker { return bytes.NewReader(stored) }, Token,
		Limits{DecompressedBytes: 1 << 20, MinBytesPerSecond: 4 << 10})
	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if err == nil {
		t.Errorf("the client read %d bytes as a whole reply", n)
	}
	if n > 1<<20 {
		t.Errorf("read %d bytes past the cap", n)
	}
	if e := <-errs; e != ErrDecompressedTooLarge {
		t.Errorf("Serve: %v", e)
	}
}

// TestSlowReaderIsCut: a client that stops reading is cut at the end of its window, long before
// it could hold the connection for ever, and a client that keeps up is not cut by the server's
// 60 s WriteTimeout.
func TestSlowReaderIsCut(t *testing.T) {
	defer func(w time.Duration) { paceWindow = w }(paceWindow)
	paceWindow = 300 * time.Millisecond
	const size = 1 << 30
	it := object("big.bin", "", nil)
	it.Size = size
	srv, errs := server(t, it, func() io.ReadSeeker { return &zeros{size: size} }, Token,
		Limits{DecompressedBytes: 1 << 40, MinBytesPerSecond: 1 << 20})

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
		t.Fatal(err)
	}
	// Read nothing more.
	start := time.Now()
	select {
	case e := <-errs:
		if e == nil {
			t.Fatal("Serve finished a gigabyte nobody read")
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("cut after %v", d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a client that does not read was never cut")
	}
}

// TestContextEndsServe: a client that goes away ends Serve.
func TestContextEndsServe(t *testing.T) {
	const size = 1 << 30
	it := object("big.bin", "", nil)
	it.Size = size
	srv, errs := server(t, it, func() io.ReadSeeker { return &zeros{size: size} }, Token, generous)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.CopyN(io.Discard, res.Body, 1<<20)
	cancel()
	res.Body.Close()
	select {
	case e := <-errs:
		if e == nil {
			t.Fatal("Serve reported a complete reply")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Serve went on after the client left")
	}
}

// TestPacingOutlastsWriteTimeout: a download that takes longer than the server's WriteTimeout
// completes while the client keeps up with the minimum rate (here a 500 ms WriteTimeout, a
// 300 ms window, and a client taking about 1.5 s).
func TestPacingOutlastsWriteTimeout(t *testing.T) {
	defer func(w time.Duration) { paceWindow = w }(paceWindow)
	paceWindow = 300 * time.Millisecond
	const size = 64 << 20
	it := object("big.bin", "", nil)
	it.Size = size
	errs := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() { errs <- err }()
		err = Serve(w, r, it, &zeros{size: size}, Token, Limits{DecompressedBytes: 1 << 40, MinBytesPerSecond: 1 << 20,
			Cut: func(e error) { err = e }})
	}))
	srv.Listener.Close()
	srv.Listener = listen(t)
	srv.Config.WriteTimeout = 500 * time.Millisecond
	srv.Start()
	defer srv.Close()

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var n int64
	for {
		m, err := io.CopyN(io.Discard, res.Body, 2<<20)
		n += m
		if err != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if n != size {
		t.Errorf("read %d of %d", n, size)
	}
	if e := <-errs; e != nil {
		t.Errorf("Serve: %v", e)
	}
}
