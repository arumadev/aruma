// Command gcs-sdk-go runs S4-B's and S4-C's scenarios with cloud.google.com/go/storage against
// a live Aruma, as adminstorage will build the client (ADR-022 §14): WithEndpoint on Aruma's
// /storage/v1/, the service account's key file, whose token_uri is Aruma's, and WithJSONReads.
// A module of its own, so the Google libraries stay out of Aruma's go.mod. The acceptance test
// TestStorageGCSWithTheAdminSDKs runs it and checks the exchanges it recorded.
//
//	ARUMA_GCS_ENDPOINT=http://127.0.0.1:<port>/storage/v1/ KEY_FILE=… BUCKET=… go run .
//
// It prints one JSON line, {"results": [{"op", "ok", "detail"}]}, and exits 1 when a step failed.
// The last step is the network guard's: the Google libraries' transports clone
// http.DefaultTransport, whose dialer here refuses and reports every connection beyond loopback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// loopbackOnly makes the default transport's dialer refuse every address beyond loopback, before
// any client clones it, and returns the guard's step: how many dials went to loopback, or an
// error naming those it refused (or saying it saw none, so it was not in the path).
func loopbackOnly() func() (string, error) {
	var (
		mu      sync.Mutex
		allowed int
		refused []string
	)
	t := http.DefaultTransport.(*http.Transport)
	dial := t.DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		ip := net.ParseIP(host)
		mu.Lock()
		ok := host == "localhost" || ip != nil && ip.IsLoopback()
		if ok {
			allowed++
		} else {
			refused = append(refused, network+" "+addr)
		}
		mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("dial %s %s: refused by the network guard", network, addr)
		}
		return dial(ctx, network, addr)
	}
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case len(refused) > 0:
			return "", fmt.Errorf("refused %d dials beyond loopback: %v", len(refused), refused)
		case allowed == 0:
			return "", errors.New("saw no dial: the clients did not go through it")
		}
		return fmt.Sprintf("%d dials, all to loopback", allowed), nil
	}
}

type result struct {
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var results []result
	failed := false
	step := func(op string, fn func() (string, error)) {
		detail, err := fn()
		if err != nil {
			failed = true
			results = append(results, result{op, false, err.Error()})
			return
		}
		results = append(results, result{op, true, detail})
	}
	guard := loopbackOnly()
	defer func() {
		step("network guard", guard)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"results": results})
		if failed {
			os.Exit(1)
		}
	}()

	client, err := storage.NewClient(ctx, option.WithEndpoint(os.Getenv("ARUMA_GCS_ENDPOINT")),
		option.WithCredentialsFile(os.Getenv("KEY_FILE")), storage.WithJSONReads())
	if err != nil {
		step("init", func() (string, error) { return "", err })
		return
	}
	defer client.Close()
	bkt := client.Bucket(os.Getenv("BUCKET"))
	table := crc32.MakeTable(crc32.Castagnoli)

	write := func(name string, data []byte, chunk int, set func(w *storage.Writer)) (*storage.ObjectAttrs, error) {
		w := bkt.Object(name).NewWriter(ctx)
		w.ChunkSize = chunk
		if set != nil {
			set(w)
		}
		if _, err := w.Write(data); err != nil {
			_ = w.Close()
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return w.Attrs(), nil
	}
	check := func(a *storage.ObjectAttrs, data []byte) error {
		if a.Size != int64(len(data)) || a.CRC32C != crc32.Checksum(data, table) || len(a.MD5) != 16 {
			return fmt.Errorf("attrs size %d crc32c %d, want %d %d", a.Size, a.CRC32C, len(data), crc32.Checksum(data, table))
		}
		return nil
	}
	read := func(o *storage.ObjectHandle, off, n int64) ([]byte, *storage.Reader, error) {
		r, err := o.NewRangeReader(ctx, off, n)
		if err != nil {
			return nil, nil, err
		}
		defer r.Close()
		b, err := io.ReadAll(r) // a whole read checks X-Goog-Hash's crc32c
		return b, r, err
	}
	notExist := func(name string) error {
		if _, err := bkt.Object(name).Attrs(ctx); !errors.Is(err, storage.ErrObjectNotExist) {
			return fmt.Errorf("%s: Attrs gives %v, want ErrObjectNotExist", name, err)
		}
		return nil
	}
	status := func(err error, code int) error {
		var e *googleapi.Error
		if !errors.As(err, &e) || e.Code != code {
			return fmt.Errorf("got %v, want a googleapi.Error %d", err, code)
		}
		return nil
	}

	small := []byte("hello aruma\n")
	// 16 MiB, Go's default ChunkSize, would make the test slow; the chunked paths take 256 KiB
	// chunks, the smallest Go sends.
	const chunk = 256 << 10
	odd := bytes.Repeat([]byte("0123456789abcdef"), (2*chunk+1000)/16)
	even := bytes.Repeat([]byte{'e'}, 2*chunk)

	step("multipart", func() (string, error) {
		a, err := write("go/small.txt", small, 0, func(w *storage.Writer) {
			w.ContentType, w.CacheControl, w.Metadata = "text/plain", "public, max-age=60", map[string]string{"k": "v"}
		})
		if err != nil {
			return "", err
		}
		if err := check(a, small); err != nil {
			return "", err
		}
		return fmt.Sprintf("generation %d", a.Generation), nil
	})
	step("multipart.default", func() (string, error) {
		// Under ChunkSize: one multipart request with Go's own crc32c in the JSON.
		a, err := write("go/default.txt", small, 16<<20, nil)
		if err != nil {
			return "", err
		}
		return a.ContentType, check(a, small)
	})
	step("resumable", func() (string, error) {
		a, err := write("go/odd.bin", odd, chunk, func(w *storage.Writer) { w.ContentType = "application/octet-stream" })
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d bytes", a.Size), check(a, odd)
	})
	step("resumable.emptyLastChunk", func() (string, error) {
		// A multiple of ChunkSize ends with an empty chunk: bytes */<size>.
		a, err := write("go/even.bin", even, chunk, nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d bytes", a.Size), check(a, even)
	})
	step("multipart.badCRC32C", func() (string, error) {
		_, err := write("go/bad-multipart.txt", small, 0, func(w *storage.Writer) {
			w.CRC32C, w.SendCRC32C = crc32.Checksum([]byte("other"), table), true
		})
		if err := status(err, http.StatusBadRequest); err != nil {
			return "", err
		}
		return "400", notExist("go/bad-multipart.txt")
	})
	step("resumable.badCRC32C", func() (string, error) {
		_, err := write("go/bad-resumable.bin", odd, chunk, func(w *storage.Writer) {
			w.CRC32C, w.SendCRC32C = crc32.Checksum([]byte("other"), table), true
		})
		if err := status(err, http.StatusBadRequest); err != nil {
			return "", err
		}
		return "400", notExist("go/bad-resumable.bin")
	})
	step("ifGenerationMatch0", func() (string, error) {
		w := bkt.Object("go/small.txt").If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
		_, _ = w.Write([]byte("again"))
		if err := status(w.Close(), http.StatusPreconditionFailed); err != nil {
			return "", err
		}
		return "412", nil
	})

	step("read", func() (string, error) {
		b, r, err := read(bkt.Object("go/odd.bin"), 0, -1)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(b, odd) || r.Attrs.Size != int64(len(odd)) || r.Attrs.Generation == 0 {
			return "", fmt.Errorf("read %d bytes, attrs %+v", len(b), r.Attrs)
		}
		return fmt.Sprintf("%d bytes, generation %d", len(b), r.Attrs.Generation), nil
	})
	step("read.generation", func() (string, error) {
		a, err := bkt.Object("go/small.txt").Attrs(ctx)
		if err != nil {
			return "", err
		}
		b, r, err := read(bkt.Object("go/small.txt").Generation(a.Generation), 0, -1)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(b, small) || r.Attrs.Generation != a.Generation || r.Attrs.Metageneration != a.Metageneration {
			return "", fmt.Errorf("read %q, attrs %+v, want generation %d", b, r.Attrs, a.Generation)
		}
		if _, err := bkt.Object("go/small.txt").Generation(a.Generation + 1).NewReader(ctx); !errors.Is(err, storage.ErrObjectNotExist) {
			return "", fmt.Errorf("another generation: %v", err)
		}
		return fmt.Sprintf("generation %d", a.Generation), nil
	})
	step("read.cacheControl", func() (string, error) {
		a, err := bkt.Object("go/small.txt").Attrs(ctx)
		if err != nil {
			return "", err
		}
		_, r, err := read(bkt.Object("go/small.txt"), 0, -1)
		if err != nil {
			return "", err
		}
		if a.CacheControl != "public, max-age=60" || r.Attrs.CacheControl != a.CacheControl ||
			r.Attrs.ContentType != a.ContentType || !r.Attrs.LastModified.Equal(a.Updated.Truncate(time.Second)) {
			return "", fmt.Errorf("reader attrs %+v, object %q %q %v", r.Attrs, a.CacheControl, a.ContentType, a.Updated)
		}
		return a.CacheControl, nil
	})
	step("read.range", func() (string, error) {
		b, r, err := read(bkt.Object("go/odd.bin"), 10, 20)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(b, odd[10:30]) || r.Attrs.Size != int64(len(odd)) || r.Attrs.StartOffset != 10 {
			return "", fmt.Errorf("range read %q, attrs %+v", b, r.Attrs)
		}
		tail, _, err := read(bkt.Object("go/odd.bin"), -5, -1)
		if err != nil || !bytes.Equal(tail, odd[len(odd)-5:]) {
			return "", fmt.Errorf("tail %q: %v", tail, err)
		}
		return "ok", nil
	})
}
