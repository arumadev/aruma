// Command go-admin runs the Admin SDK for Go (firebase.google.com/go/v4 4.22.0, which uses
// cloud.google.com/go/storage 1.62.1) against a Storage server, for capture.mjs.
//
// It talks to whatever STORAGE_EMULATOR_HOST names (capture.mjs points it at its recording
// proxy) and prints one JSON line per step: {"step", "ok"} or {"step", "error"}. Its first
// argument is the bucket, the second the object prefix (aruma-ref/<run>/go).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	gcs "cloud.google.com/go/storage"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/iterator"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go-admin <bucket> <prefix>")
		os.Exit(2)
	}
	bucketName, prefix := os.Args[1], os.Args[2]
	ctx := context.Background()
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: "demo-aruma-ref", StorageBucket: bucketName})
	if err != nil {
		fail("NewApp", err)
	}
	client, err := app.Storage(ctx)
	if err != nil {
		fail("Storage", err)
	}
	b, err := client.DefaultBucket()
	if err != nil {
		fail("DefaultBucket", err)
	}

	write := func(name string, data []byte, chunk int, set func(w *gcs.Writer)) (any, error) {
		w := b.Object(name).NewWriter(ctx)
		w.ChunkSize = chunk
		if set != nil {
			set(w)
		}
		if _, err := w.Write(data); err != nil {
			w.Close()
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return attrs(w.Attrs()), nil
	}

	step("Writer small, ChunkSize 0 (single request)", func() (any, error) {
		return write(prefix+"/small.txt", []byte("go small"), 0, func(w *gcs.Writer) {
			w.ContentType = "text/plain"
			w.Metadata = map[string]string{"who": "go"}
		})
	})
	step("Writer small, default ChunkSize (16 MiB)", func() (any, error) {
		return write(prefix+"/default.txt", []byte("go default"), 16<<20, nil)
	})
	step("Writer 9 MiB, ChunkSize 4 MiB (resumable, 3 chunks)", func() (any, error) {
		return write(prefix+"/big.bin", bytes.Repeat([]byte("G"), 9<<20), 4<<20, nil)
	})
	step("Attrs", func() (any, error) {
		a, err := b.Object(prefix + "/small.txt").Attrs(ctx)
		return attrs(a), err
	})
	step("Attrs of a missing object", func() (any, error) {
		a, err := b.Object(prefix + "/missing.txt").Attrs(ctx)
		return attrs(a), err
	})
	step("Update (CacheControl, metadata)", func() (any, error) {
		a, err := b.Object(prefix+"/small.txt").Update(ctx, gcs.ObjectAttrsToUpdate{
			CacheControl: "no-store",
			Metadata:     map[string]string{"added": "1"},
		})
		return attrs(a), err
	})
	step("NewReader", func() (any, error) {
		r, err := b.Object(prefix + "/small.txt").NewReader(ctx)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		return string(data), err
	})
	step("NewRangeReader 0..3", func() (any, error) {
		r, err := b.Object(prefix+"/small.txt").NewRangeReader(ctx, 0, 3)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		return string(data), err
	})
	step("Objects with Prefix and Delimiter", func() (any, error) {
		var out []string
		it := b.Objects(ctx, &gcs.Query{Prefix: prefix + "/", Delimiter: "/"})
		for {
			a, err := it.Next()
			if errors.Is(err, iterator.Done) {
				return out, nil
			}
			if err != nil {
				return out, err
			}
			out = append(out, a.Name+a.Prefix)
		}
	})
	step("CopierFrom (rewrite)", func() (any, error) {
		a, err := b.Object(prefix + "/copy.txt").CopierFrom(b.Object(prefix + "/small.txt")).Run(ctx)
		return attrs(a), err
	})
	step("Writer with DoesNotExist precondition on an existing object", func() (any, error) {
		w := b.Object(prefix + "/small.txt").If(gcs.Conditions{DoesNotExist: true}).NewWriter(ctx)
		w.ChunkSize = 0
		w.Write([]byte("again"))
		if err := w.Close(); err != nil {
			return nil, err
		}
		return attrs(w.Attrs()), nil
	})
	step("Delete a missing object", func() (any, error) {
		return nil, b.Object(prefix + "/missing.txt").Delete(ctx)
	})
	step("Bucket Attrs", func() (any, error) {
		a, err := b.Attrs(ctx)
		if a == nil {
			return nil, err
		}
		return map[string]any{"name": a.Name, "location": a.Location}, err
	})
	for _, n := range []string{"small.txt", "default.txt", "big.bin", "copy.txt"} {
		n := n
		step("Delete "+n, func() (any, error) { return nil, b.Object(prefix + "/" + n).Delete(ctx) })
	}
}

func attrs(a *gcs.ObjectAttrs) any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"name": a.Name, "size": a.Size, "contentType": a.ContentType, "generation": a.Generation,
		"metageneration": a.Metageneration, "md5": a.MD5, "crc32c": a.CRC32C, "etag": a.Etag,
		"metadata": a.Metadata, "cacheControl": a.CacheControl, "created": a.Created, "updated": a.Updated,
	}
}

func step(name string, fn func() (any, error)) {
	v, err := fn()
	out := map[string]any{"step": name}
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["ok"] = v
	}
	line, _ := json.Marshal(out)
	fmt.Println(string(line))
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}
