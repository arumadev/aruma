// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Command admin-go runs one Admin SDK configuration for Go against the S0-B recording stub and
// prints one JSON line: {"scenario", "results": [{"op", "ok", "detail"}]}. A module of its own, so
// that firebase-admin-go and cloud.google.com/go/storage stay out of Aruma's go.mod.
//
//	KEY_FILE=… BUCKET=… STUB_URL=http://127.0.0.1:8820 go run . <scenario>
//
// The runner (../admin-go.sh) sets HTTPS_PROXY to the stub's guard proxy, so a request that
// leaves loopback is refused and recorded.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type result struct {
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func main() {
	scenario := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var results []result
	step := func(op string, fn func() (string, error)) {
		detail, err := fn()
		if err != nil {
			msg := err.Error()
			if len(msg) > 240 {
				msg = msg[:240]
			}
			results = append(results, result{op, false, msg})
			return
		}
		results = append(results, result{op, true, detail})
	}
	defer func() {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"scenario": scenario, "results": results})
	}()

	keyFile, bucketName, stubURL := os.Getenv("KEY_FILE"), os.Getenv("BUCKET"), os.Getenv("STUB_URL")
	var bkt *storage.BucketHandle
	step("init", func() (string, error) {
		var err error
		creds := option.WithCredentialsFile(keyFile)
		switch scenario {
		case "admin-default", "admin-emulator-http", "admin-emulator-https":
			bkt, err = adminBucket(ctx, bucketName, creds)
		case "admin-endpoint":
			bkt, err = adminBucket(ctx, bucketName, creds, option.WithEndpoint(stubURL+"/storage/v1/"))
		case "admin-endpoint-jsonreads":
			bkt, err = adminBucket(ctx, bucketName, creds, option.WithEndpoint(stubURL+"/storage/v1/"), storage.WithJSONReads())
		case "admin-endpoint-prefix":
			bkt, err = adminBucket(ctx, bucketName, creds, option.WithEndpoint(stubURL+"/storage.googleapis.com/storage/v1/"), storage.WithJSONReads())
		case "gcs-endpoint":
			var c *storage.Client
			if c, err = storage.NewClient(ctx, creds, option.WithEndpoint(stubURL+"/storage/v1/")); err == nil {
				bkt = c.Bucket(bucketName)
			}
		case "gcs-endpoint-jsonreads":
			// A storage-only client, apart from the firebase.App: what an adminstorage helper returns.
			var c *storage.Client
			if c, err = storage.NewClient(ctx, creds, option.WithEndpoint(stubURL+"/storage/v1/"), storage.WithJSONReads()); err == nil {
				bkt = c.Bucket(bucketName)
			}
		case "admin-endpoint-messaging":
			// The same WithEndpoint on firebase.NewApp, seen from another service of the app.
			var app *firebase.App
			if app, err = firebase.NewApp(ctx, &firebase.Config{ProjectID: "s0path", StorageBucket: bucketName}, creds, option.WithEndpoint(stubURL+"/storage/v1/")); err == nil {
				var m *messaging.Client
				if m, err = app.Messaging(ctx); err == nil {
					_, sendErr := m.Send(ctx, &messaging.Message{Topic: "s0path", Data: map[string]string{"k": "v"}})
					return fmt.Sprintf("messaging.Send with WithEndpoint on the app: %v", sendErr), nil
				}
			}
		default:
			err = fmt.Errorf("unknown scenario %q", scenario)
		}
		return fmt.Sprintf("STORAGE_EMULATOR_HOST=%q", os.Getenv("STORAGE_EMULATOR_HOST")), err
	})
	if bkt == nil {
		return
	}
	small := []byte("hello aruma\n")
	big := bytes.Repeat([]byte{7}, 3*1024*1024+17)
	write := func(name string, data []byte, chunk int, ct string) (string, error) {
		w := bkt.Object(name).NewWriter(ctx)
		w.ChunkSize = chunk
		w.ContentType = ct
		if _, err := w.Write(data); err != nil {
			return "", err
		}
		return "", w.Close()
	}
	step("upload.small", func() (string, error) { return write("s0/small.txt", small, 0, "text/plain") })
	// A chunk size below the object size makes the Writer use a resumable session.
	step("upload.resumable3MB", func() (string, error) {
		return write("s0/big.bin", big, 256*1024, "application/octet-stream")
	})
	step("getMetadata", func() (string, error) {
		a, err := bkt.Object("s0/small.txt").Attrs(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("size=%d", a.Size), nil
	})
	step("download", func() (string, error) {
		r, err := bkt.Object("s0/big.bin").NewReader(ctx)
		if err != nil {
			return "", err
		}
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(got, big) {
			return "", fmt.Errorf("downloaded %d bytes that differ", len(got))
		}
		return fmt.Sprintf("%d bytes", len(got)), nil
	})
	step("list", func() (string, error) {
		it := bkt.Objects(ctx, &storage.Query{Prefix: "s0/"})
		n := 0
		for {
			_, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return "", err
			}
			n++
		}
		return fmt.Sprintf("%d files", n), nil
	})
	step("delete", func() (string, error) {
		var errs []string
		for _, n := range []string{"s0/small.txt", "s0/big.bin"} {
			if err := bkt.Object(n).Delete(ctx); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return "", errors.New(strings.Join(errs, "; "))
		}
		return "", nil
	})
}

// adminBucket is firebase-admin-go's path: app.Storage (which hands the app's options to
// cloud.google.com/go/storage), then the configured default bucket.
func adminBucket(ctx context.Context, bucket string, opts ...option.ClientOption) (*storage.BucketHandle, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: "s0path", StorageBucket: bucket}, opts...)
	if err != nil {
		return nil, err
	}
	c, err := app.Storage(ctx)
	if err != nil {
		return nil, err
	}
	return c.DefaultBucket()
}
