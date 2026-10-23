// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Command stub is a recording stand-in for an Aruma server with Cloud Storage, used by the S0-B
// experiments (docs/research/storage-production-path.md). It is not Aruma and checks nothing: it
// answers just enough of the two Storage protocols for an official SDK to finish getMetadata,
// getDownloadURL, uploads (multipart and resumable), downloads, list and delete, and writes one
// JSON line per request saying where the request arrived and in what shape.
//
// Protocols, under any of the path prefixes a redirect library adds (the original host as the
// first segment, ADR-014) or none (the emulator layout):
//
//	/v0/b/{bucket}/o[/{name}]                     Firebase Storage REST (client SDKs)
//	/storage/v1/b/{bucket}[/o[/{name}]]           GCS JSON API (Admin SDKs)
//	/upload/storage/v1/b/{bucket}/o               GCS uploads
//	/download/storage/v1/b/{bucket}/o/{name}      GCS media
//	/{bucket}/{name}                              GCS XML reads (Go), for a bucket the stub knows
//	/oauth2/v1/token, /token                      a token endpoint that grants anything
//
// The log never holds a credential: the Authorization header is reduced to its scheme and length,
// and the token, key and access_token query values are replaced.
//
//	go run ./test/experiments/storage-path/stub -http 127.0.0.1:8820 -https 127.0.0.1:8821 \
//	    -guard 127.0.0.1:8822 -certdir .generated/tls -log .generated/requests.jsonl
//	go run ./test/experiments/storage-path/stub summarize .generated/requests.jsonl
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// exactHeaders makes setHeader keep Google's spelling of a header name instead of Go's canonical one.
var exactHeaders = true

// setHeader sets a response header, spelled as Google spells it unless -header-case=canonical.
// HTTP/1.1 header names are case-insensitive, but a client may look them up in a case-sensitive map.
func setHeader(h http.Header, name, value string) {
	if exactHeaders {
		delete(h, http.CanonicalHeaderKey(name))
		h[name] = []string{value}
		return
	}
	h.Set(name, value)
}

// prefixes are the path prefixes a redirect library adds: the Google host the SDK asked for.
var prefixes = []string{"/firebasestorage.googleapis.com", "/storage.googleapis.com", "/www.googleapis.com"}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "summarize" {
		if err := summarize(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	httpAddr := flag.String("http", "127.0.0.1:8820", "plain HTTP listener (empty: none)")
	httpsAddr := flag.String("https", "127.0.0.1:8821", "HTTPS listener (empty: none)")
	guardAddr := flag.String("guard", "", "a forward proxy that refuses and records every request (for HTTPS_PROXY)")
	certDir := flag.String("certdir", "", "where ca.pem, server.pem and server-key.pem are kept (made when missing)")
	logPath := flag.String("log", "", "JSON lines, one per request (default stdout)")
	uploadURL := flag.String("upload-url", "self", "X-Goog-Upload-URL of a resumable upload: self (this server, absolute) or google (https://firebasestorage.googleapis.com)")
	headerCase := flag.String("header-case", "exact", "response header names: exact (Google's spelling, e.g. X-Goog-Upload-URL) or canonical (Go's, X-Goog-Upload-Url)")
	buckets := flag.String("buckets", "", "comma-separated buckets the XML read path /{bucket}/{name} answers for, besides those with objects")
	flag.Parse()

	var out io.Writer = os.Stdout
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	if *uploadURL != "self" && *uploadURL != "google" {
		log.Fatalf("-upload-url must be self or google")
	}
	s := newServer(out, *uploadURL)
	exactHeaders = *headerCase == "exact"
	for _, b := range strings.Split(*buckets, ",") {
		if b = strings.TrimSpace(b); b != "" {
			s.knownBuckets[b] = true
		}
	}

	errc := make(chan error, 3)
	if *httpAddr != "" {
		go func() { errc <- serve(*httpAddr, s, nil) }()
		log.Printf("stub: http://%s", *httpAddr)
	}
	if *httpsAddr != "" {
		if *certDir == "" {
			log.Fatal("-https needs -certdir")
		}
		cert, err := loadOrMakeCert(*certDir)
		if err != nil {
			log.Fatal(err)
		}
		go func() { errc <- serve(*httpsAddr, s, &tls.Config{Certificates: []tls.Certificate{cert}}) }()
		log.Printf("stub: https://%s (CA %s)", *httpsAddr, filepath.Join(*certDir, "ca.pem"))
	}
	if *guardAddr != "" {
		go func() { errc <- serve(*guardAddr, guardProxy{s}, nil) }()
		log.Printf("stub: guard proxy http://%s", *guardAddr)
	}
	log.Fatal(<-errc)
}

func serve(addr string, h http.Handler, tc *tls.Config) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if tc != nil {
		tc.NextProtos = []string{"h2", "http/1.1"}
		return srv.Serve(tls.NewListener(ln, tc))
	}
	return srv.Serve(ln)
}

// entry is one log line.
type entry struct {
	Time     string            `json:"time"`
	Listener string            `json:"listener"` // http, https or guard
	Method   string            `json:"method"`
	Host     string            `json:"host"`
	Prefix   string            `json:"prefix,omitempty"` // the Google host a redirect put first
	Path     string            `json:"path"`             // after the prefix, still escaped
	Query    string            `json:"query,omitempty"`  // with secrets replaced
	Op       string            `json:"op"`
	Auth     string            `json:"auth"` // "none" or "<scheme>(<length>)"
	Headers  map[string]string `json:"headers,omitempty"`
	BodyIn   int64             `json:"bodyIn"`
	Status   int               `json:"status"`
	Note     string            `json:"note,omitempty"`
}

type object struct {
	bucket, name       string
	contentType        string
	data               []byte
	generation         int64
	metageneration     int64
	custom             map[string]string
	token              string
	created, updated   time.Time
	cacheControl       string
	contentDisposition string
}

type upload struct {
	kind         string // fb or gcs
	bucket, name string
	contentType  string
	custom       map[string]string
	total        int64 // -1 when unknown
	data         []byte
	done         *object
}

type server struct {
	mu           sync.Mutex
	enc          *json.Encoder
	uploadURL    string
	objects      map[string]map[string]*object
	uploads      map[string]*upload
	knownBuckets map[string]bool
	seq          int64
}

func newServer(out io.Writer, uploadURL string) *server {
	return &server{
		enc:          json.NewEncoder(out),
		uploadURL:    uploadURL,
		objects:      map[string]map[string]*object{},
		uploads:      map[string]*upload{},
		knownBuckets: map[string]bool{},
	}
}

func (s *server) write(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(e)
}

// recorder keeps the status a handler wrote.
type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// loggedHeaders are recorded as sent; everything else is left out.
var loggedHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Range", "User-Agent", "Origin",
	"X-Firebase-Storage-Version", "X-Firebase-Gmpid", "X-Goog-Api-Client", "X-Goog-Upload-Protocol",
	"X-Goog-Upload-Command", "X-Goog-Upload-Offset", "X-Goog-Upload-Header-Content-Type",
	"X-Goog-Upload-Header-Content-Length", "X-Goog-Upload-Content-Type", "X-Goog-Upload-Content-Length",
	"X-Goog-User-Project", "X-Goog-Gcs-Idempotency-Token", "X-Guploader-No-308",
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	e := &entry{
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
		Listener: "http",
		Method:   r.Method,
		Host:     r.Host,
		Query:    redactQuery(r.URL.RawQuery),
		Auth:     authSummary(r.Header.Get("Authorization")),
		Headers:  map[string]string{},
	}
	if r.TLS != nil {
		e.Listener = "https"
	}
	for _, h := range loggedHeaders {
		if v := r.Header.Get(h); v != "" {
			e.Headers[h] = v
		}
	}
	if r.Header.Get("X-Firebase-Appcheck") != "" {
		e.Headers["X-Firebase-Appcheck"] = "<present>"
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p+"/") {
			e.Prefix = strings.TrimPrefix(p, "/")
			path = strings.TrimPrefix(path, p)
			break
		}
	}
	e.Path = path
	body, err := io.ReadAll(r.Body)
	e.BodyIn = int64(len(body))
	rec := &recorder{ResponseWriter: w}
	defer func() {
		e.Status = rec.status
		s.write(e)
	}()
	if err != nil {
		e.Op, e.Note = "error", err.Error()
		http.Error(rec, err.Error(), http.StatusBadRequest)
		return
	}
	cors(rec, r)
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		e.Op = "cors.preflight"
		e.Headers["Access-Control-Request-Method"] = r.Header.Get("Access-Control-Request-Method")
		e.Headers["Access-Control-Request-Headers"] = r.Header.Get("Access-Control-Request-Headers")
		rec.WriteHeader(http.StatusNoContent)
		return
	}
	base := scheme(r) + "://" + r.Host
	if e.Prefix != "" {
		base += "/" + e.Prefix
	}
	c := &call{s: s, w: rec, r: r, e: e, body: body, base: base}
	switch {
	case path == "/oauth2/v1/token" || path == "/token" || path == "/oauth2/v4/token":
		c.token()
	case strings.HasPrefix(path, "/v0/b/"):
		c.firebase(strings.TrimPrefix(path, "/v0/b/"))
	case strings.HasPrefix(path, "/storage/v1/b/"):
		c.gcs(strings.TrimPrefix(path, "/storage/v1/b/"), "json")
	case strings.HasPrefix(path, "/b/"):
		// @google-cloud/storage with STORAGE_EMULATOR_HOST uses the variable as its base URL as is,
		// without /storage/v1.
		c.e.Note = "GCS JSON without /storage/v1"
		c.gcs(strings.TrimPrefix(path, "/b/"), "json")
	case strings.HasPrefix(path, "/download/storage/v1/b/"):
		c.gcs(strings.TrimPrefix(path, "/download/storage/v1/b/"), "download")
	case strings.HasPrefix(path, "/upload/storage/v1/b/"):
		c.gcsUpload(strings.TrimPrefix(path, "/upload/storage/v1/b/"))
	case path == "/healthz":
		e.Op = "healthz"
		io.WriteString(rec, "ok\n")
	default:
		c.xmlRead(strings.TrimPrefix(path, "/"))
	}
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// cors answers like Google's storage hosts: any origin, and the upload headers exposed.
func cors(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Expose-Headers", "X-Goog-Upload-URL, X-Goog-Upload-Status, X-Goog-Upload-Size-Received, X-Goog-Upload-Chunk-Granularity, X-Goog-Hash, Content-Range, Location, Range")
	if r.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
			h.Set("Access-Control-Allow-Headers", req)
		}
		h.Set("Access-Control-Max-Age", "3600")
	}
}

func authSummary(v string) string {
	if v == "" {
		return "none"
	}
	scheme, rest, _ := strings.Cut(v, " ")
	return fmt.Sprintf("%s(%d)", scheme, len(rest))
}

func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		k, _, _ := strings.Cut(p, "=")
		switch k {
		case "token", "key", "access_token", "assertion", "client_secret", "refresh_token":
			parts[i] = k + "=<redacted>"
		}
	}
	return strings.Join(parts, "&")
}

// call is one request being answered.
type call struct {
	s    *server
	w    http.ResponseWriter
	r    *http.Request
	e    *entry
	body []byte
	base string // scheme://host[/prefix] as the client addressed this server
}

func (c *call) json(status int, v any) {
	c.w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	c.w.WriteHeader(status)
	_ = json.NewEncoder(c.w).Encode(v)
}

func (c *call) fail(status int, op, msg string) {
	if c.e.Op == "" {
		c.e.Op = op
	}
	c.e.Note = msg
	c.json(status, map[string]any{"error": map[string]any{"code": status, "message": msg}})
}

func (c *call) token() {
	c.e.Op = "token"
	form, _ := url.ParseQuery(string(c.body))
	c.e.Note = "grant_type=" + form.Get("grant_type")
	c.s.mu.Lock()
	c.s.seq++
	n := c.s.seq
	c.s.mu.Unlock()
	c.json(http.StatusOK, map[string]any{"access_token": fmt.Sprintf("stub-access-token-%d", n), "expires_in": 3600, "token_type": "Bearer"})
}

// splitObject splits "{bucket}/o/{name}" (escaped) into its parts. hasO reports a "/o" segment.
func splitObject(rest string) (bucket string, hasO bool, name string, err error) {
	b, after, found := strings.Cut(rest, "/")
	if bucket, err = url.PathUnescape(b); err != nil {
		return
	}
	if !found {
		return
	}
	if after == "o" {
		return bucket, true, "", nil
	}
	if strings.HasPrefix(after, "o/") {
		name, err = url.PathUnescape(strings.TrimPrefix(after, "o/"))
		return bucket, true, name, err
	}
	return bucket, false, "", fmt.Errorf("unknown path %q", after)
}

func (c *call) firebase(rest string) {
	bucket, hasO, name, err := splitObject(rest)
	q := c.r.URL.Query()
	switch {
	case err != nil || !hasO:
		c.fail(http.StatusNotFound, "fb.unknown", fmt.Sprint("bad path: ", err))
	case q.Get("upload_id") != "":
		c.fbResumable(q.Get("upload_id"))
	case name == "" && c.r.Method == http.MethodGet:
		c.list("fb.list", bucket, false)
	case c.r.Method == http.MethodPost:
		// The web and Android post to /o?name=…; iOS to the object's own path, /o/{name}?name=….
		if name != "" {
			c.e.Note = "upload posted to the object path"
		}
		if n := q.Get("name"); n != "" {
			name = n
		}
		c.fbUpload(bucket, name)
	case name == "":
		c.fail(http.StatusMethodNotAllowed, "fb.unknown", "method")
	case c.r.Method == http.MethodGet && q.Get("alt") == "media":
		c.media("fb.media", bucket, name)
	case c.r.Method == http.MethodGet:
		c.e.Op = "fb.getMetadata"
		if o := c.s.get(bucket, name); o != nil {
			c.json(http.StatusOK, c.fbResource(o))
		} else {
			c.fail(http.StatusNotFound, "", "Not Found.")
		}
	case c.r.Method == http.MethodDelete:
		c.delete("fb.delete", bucket, name)
	case c.r.Method == http.MethodPatch:
		c.e.Op = "fb.updateMetadata"
		o := c.s.patch(bucket, name, c.body)
		if o == nil {
			c.fail(http.StatusNotFound, "", "Not Found.")
			return
		}
		c.json(http.StatusOK, c.fbResource(o))
	default:
		c.fail(http.StatusMethodNotAllowed, "fb.unknown", "method")
	}
}

// metadataBody is the JSON metadata both protocols send with an upload.
type metadataBody struct {
	Name               string            `json:"name"`
	ContentType        string            `json:"contentType"`
	CacheControl       string            `json:"cacheControl"`
	ContentDisposition string            `json:"contentDisposition"`
	Metadata           map[string]string `json:"metadata"`
}

func parseMetadata(b []byte) metadataBody {
	var m metadataBody
	_ = json.Unmarshal(b, &m)
	return m
}

func (c *call) fbUpload(bucket, name string) {
	proto := strings.ToLower(c.r.Header.Get("X-Goog-Upload-Protocol"))
	cmd := strings.ToLower(c.r.Header.Get("X-Goog-Upload-Command"))
	switch {
	case proto == "multipart":
		c.e.Op = "fb.upload.multipart"
		md, data, err := parseMultipart(c.r.Header.Get("Content-Type"), c.body)
		if err != nil {
			c.fail(http.StatusBadRequest, "", err.Error())
			return
		}
		if name == "" {
			name = md.Name
		}
		o := c.s.put(bucket, name, md, data)
		c.json(http.StatusOK, c.fbResource(o))
	case proto == "resumable" && cmd == "start":
		c.e.Op = "fb.upload.start"
		md := parseMetadata(c.body)
		if name == "" {
			name = md.Name
		}
		u := &upload{kind: "fb", bucket: bucket, name: name, contentType: md.ContentType, custom: md.Metadata, total: -1}
		for _, h := range []string{"X-Goog-Upload-Header-Content-Length", "X-Goog-Upload-Content-Length"} {
			if n, err := strconv.ParseInt(c.r.Header.Get(h), 10, 64); err == nil {
				u.total = n
			}
		}
		for _, h := range []string{"X-Goog-Upload-Header-Content-Type", "X-Goog-Upload-Content-Type"} {
			if v := c.r.Header.Get(h); v != "" && u.contentType == "" {
				u.contentType = v
			}
		}
		id := c.s.newUpload(u)
		target := c.base
		if c.s.uploadURL == "google" {
			target = "https://firebasestorage.googleapis.com"
		}
		loc := fmt.Sprintf("%s/v0/b/%s/o?name=%s&upload_id=%s&upload_protocol=resumable",
			target, url.PathEscape(bucket), url.QueryEscape(name), id)
		c.e.Note = "X-Goog-Upload-URL=" + redactQuery(loc)
		h := c.w.Header()
		setHeader(h, "X-Goog-Upload-Status", "active")
		setHeader(h, "X-Goog-Upload-URL", loc)
		setHeader(h, "X-Goog-Upload-Chunk-Granularity", "262144")
		setHeader(h, "X-Goog-Upload-Control-URL", loc)
		c.w.WriteHeader(http.StatusOK)
	default:
		// A plain body upload (no protocol header): the media is the body.
		c.e.Op = "fb.upload.raw"
		ct := c.r.Header.Get("Content-Type")
		o := c.s.put(bucket, name, metadataBody{ContentType: ct}, c.body)
		c.json(http.StatusOK, c.fbResource(o))
	}
}

func (c *call) fbResumable(id string) {
	c.s.mu.Lock()
	u := c.s.uploads[id]
	c.s.mu.Unlock()
	cmds := map[string]bool{}
	for _, t := range strings.Split(strings.ToLower(c.r.Header.Get("X-Goog-Upload-Command")), ",") {
		cmds[strings.TrimSpace(t)] = true
	}
	h := c.w.Header()
	if u == nil {
		c.fail(http.StatusNotFound, "fb.upload.unknown", "no such upload")
		return
	}
	switch {
	case cmds["query"]:
		c.e.Op = "fb.upload.query"
	case cmds["cancel"]:
		c.e.Op = "fb.upload.cancel"
		setHeader(h, "X-Goog-Upload-Status", "cancelled")
		c.w.WriteHeader(http.StatusOK)
		return
	case cmds["upload"] || cmds["finalize"]:
		c.e.Op = "fb.upload.chunk"
		if cmds["finalize"] {
			c.e.Op = "fb.upload.chunk+finalize"
			if !cmds["upload"] {
				c.e.Op = "fb.upload.finalize"
			}
		}
		c.s.mu.Lock()
		off, err := strconv.ParseInt(c.r.Header.Get("X-Goog-Upload-Offset"), 10, 64)
		if err != nil {
			off = int64(len(u.data))
		}
		if u.done == nil && cmds["upload"] {
			if off > int64(len(u.data)) {
				c.s.mu.Unlock()
				c.fail(http.StatusBadRequest, "", "offset beyond the received bytes")
				return
			}
			u.data = append(u.data[:off], c.body...)
		}
		c.s.mu.Unlock()
		if cmds["finalize"] {
			c.s.mu.Lock()
			if u.done == nil {
				c.s.mu.Unlock()
				u.done = c.s.put(u.bucket, u.name, metadataBody{ContentType: u.contentType, Metadata: u.custom}, u.data)
			} else {
				c.s.mu.Unlock()
			}
			setHeader(h, "X-Goog-Upload-Status", "final")
			c.json(http.StatusOK, c.fbResource(u.done))
			return
		}
	default:
		c.fail(http.StatusBadRequest, "fb.upload.unknown", "unknown X-Goog-Upload-Command")
		return
	}
	c.s.mu.Lock()
	n, final := len(u.data), u.done != nil
	c.s.mu.Unlock()
	if final {
		setHeader(h, "X-Goog-Upload-Status", "final")
	} else {
		setHeader(h, "X-Goog-Upload-Status", "active")
	}
	setHeader(h, "X-Goog-Upload-Size-Received", strconv.Itoa(n))
	c.w.WriteHeader(http.StatusOK)
}

func parseMultipart(contentType string, body []byte) (metadataBody, []byte, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return metadataBody{}, nil, fmt.Errorf("not multipart: %q", contentType)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var md metadataBody
	var data []byte
	for i := 0; ; i++ {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return md, nil, err
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return md, nil, err
		}
		if i == 0 {
			md = parseMetadata(b)
		} else {
			data = b
			if md.ContentType == "" {
				md.ContentType = p.Header.Get("Content-Type")
			}
		}
	}
	return md, data, nil
}

func (c *call) list(op, bucket string, gcs bool) {
	c.e.Op = op
	q := c.r.URL.Query()
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	c.s.mu.Lock()
	var names []string
	for n := range c.s.objects[bucket] {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	c.s.mu.Unlock()
	sort.Strings(names)
	items := []any{}
	seen := map[string]bool{}
	prefixesOut := []string{}
	for _, n := range names {
		if delim != "" {
			if i := strings.Index(n[len(prefix):], delim); i >= 0 {
				p := n[:len(prefix)+i+len(delim)]
				if !seen[p] {
					seen[p] = true
					prefixesOut = append(prefixesOut, p)
				}
				continue
			}
		}
		o := c.s.get(bucket, n)
		if o == nil {
			continue
		}
		if gcs {
			items = append(items, c.gcsResource(o))
		} else {
			items = append(items, map[string]any{"name": n, "bucket": bucket})
		}
	}
	out := map[string]any{"prefixes": prefixesOut, "items": items}
	if gcs {
		out["kind"] = "storage#objects"
	}
	c.json(http.StatusOK, out)
}

func (c *call) delete(op, bucket, name string) {
	c.e.Op = op
	c.s.mu.Lock()
	_, ok := c.s.objects[bucket][name]
	delete(c.s.objects[bucket], name)
	c.s.mu.Unlock()
	if !ok {
		c.fail(http.StatusNotFound, "", "Not Found.")
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// media writes an object's bytes, honouring a single Range.
func (c *call) media(op, bucket, name string) {
	c.e.Op = op
	o := c.s.get(bucket, name)
	if o == nil {
		c.fail(http.StatusNotFound, "", "Not Found.")
		return
	}
	h := c.w.Header()
	h.Set("Content-Type", o.contentType)
	setHeader(h, "X-Goog-Hash", "crc32c="+crc32cB64(o.data)+",md5="+md5B64(o.data))
	h.Set("X-Goog-Generation", strconv.FormatInt(o.generation, 10))
	h.Set("X-Goog-Metageneration", strconv.FormatInt(o.metageneration, 10))
	h.Set("X-Goog-Stored-Content-Length", strconv.Itoa(len(o.data)))
	h.Set("X-Goog-Stored-Content-Encoding", "identity")
	h.Set("Last-Modified", o.updated.Format(http.TimeFormat))
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Disposition", o.contentDisposition)
	data, status := o.data, http.StatusOK
	if rg := c.r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
		from, err1 := strconv.ParseInt(a, 10, 64)
		to, err2 := strconv.ParseInt(b, 10, 64)
		if err2 != nil || to >= int64(len(o.data)) {
			to = int64(len(o.data)) - 1
		}
		if err1 == nil && from <= to {
			data, status = o.data[from:to+1], http.StatusPartialContent
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(o.data)))
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	c.w.WriteHeader(status)
	if c.r.Method != http.MethodHead {
		_, _ = c.w.Write(data)
	}
}

func (c *call) gcs(rest, kind string) {
	bucket, hasO, name, err := splitObject(rest)
	q := c.r.URL.Query()
	switch {
	case err != nil:
		c.fail(http.StatusNotFound, "gcs.unknown", err.Error())
	case !hasO:
		c.e.Op = "gcs.getBucket"
		c.json(http.StatusOK, map[string]any{"kind": "storage#bucket", "id": bucket, "name": bucket, "location": "US", "storageClass": "STANDARD"})
	case name == "" && c.r.Method == http.MethodGet:
		c.list("gcs.list", bucket, true)
	case name == "":
		c.fail(http.StatusMethodNotAllowed, "gcs.unknown", "method")
	case (c.r.Method == http.MethodGet || c.r.Method == http.MethodHead) && (q.Get("alt") == "media" || kind == "download"):
		c.media("gcs.media", bucket, name)
	case c.r.Method == http.MethodGet:
		c.e.Op = "gcs.get"
		if o := c.s.get(bucket, name); o != nil {
			c.json(http.StatusOK, c.gcsResource(o))
		} else {
			c.fail(http.StatusNotFound, "", "No such object: "+bucket+"/"+name)
		}
	case c.r.Method == http.MethodDelete:
		c.delete("gcs.delete", bucket, name)
	case c.r.Method == http.MethodPatch || c.r.Method == http.MethodPut:
		c.e.Op = "gcs.patch"
		o := c.s.patch(bucket, name, c.body)
		if o == nil {
			c.fail(http.StatusNotFound, "", "No such object: "+bucket+"/"+name)
			return
		}
		c.json(http.StatusOK, c.gcsResource(o))
	default:
		c.fail(http.StatusMethodNotAllowed, "gcs.unknown", "method")
	}
}

func (c *call) gcsUpload(rest string) {
	bucket, _, _, err := splitObject(rest)
	q := c.r.URL.Query()
	if err != nil {
		c.fail(http.StatusNotFound, "gcs.upload.unknown", err.Error())
		return
	}
	if id := q.Get("upload_id"); id != "" {
		c.gcsResumable(id)
		return
	}
	switch q.Get("uploadType") {
	case "multipart":
		c.e.Op = "gcs.upload.multipart"
		md, data, err := parseMultipart(c.r.Header.Get("Content-Type"), c.body)
		if err != nil {
			c.fail(http.StatusBadRequest, "", err.Error())
			return
		}
		name := q.Get("name")
		if name == "" {
			name = md.Name
		}
		c.json(http.StatusOK, c.gcsResource(c.s.put(bucket, name, md, data)))
	case "media":
		c.e.Op = "gcs.upload.media"
		c.json(http.StatusOK, c.gcsResource(c.s.put(bucket, q.Get("name"), metadataBody{ContentType: c.r.Header.Get("Content-Type")}, c.body)))
	case "resumable":
		c.e.Op = "gcs.upload.start"
		md := parseMetadata(c.body)
		name := q.Get("name")
		if name == "" {
			name = md.Name
		}
		ct := md.ContentType
		if ct == "" {
			ct = c.r.Header.Get("X-Upload-Content-Type")
		}
		id := c.s.newUpload(&upload{kind: "gcs", bucket: bucket, name: name, contentType: ct, custom: md.Metadata, total: -1})
		loc := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s&upload_id=%s",
			c.base, url.PathEscape(bucket), url.QueryEscape(name), id)
		c.e.Note = "Location=" + loc
		c.w.Header().Set("Location", loc)
		setHeader(c.w.Header(), "X-GUploader-UploadID", id)
		c.w.WriteHeader(http.StatusOK)
	default:
		c.fail(http.StatusBadRequest, "gcs.upload.unknown", "uploadType")
	}
}

// gcsResumable takes a PUT of a GCS resumable session: Content-Range "bytes a-b/total",
// "bytes a-b/*" or "bytes */total" (a status query).
func (c *call) gcsResumable(id string) {
	c.s.mu.Lock()
	u := c.s.uploads[id]
	c.s.mu.Unlock()
	if u == nil {
		c.fail(http.StatusNotFound, "gcs.upload.unknown", "no such upload")
		return
	}
	if c.r.Method == http.MethodDelete {
		c.e.Op = "gcs.upload.cancel"
		c.w.WriteHeader(499)
		return
	}
	c.e.Op = "gcs.upload.put"
	cr := strings.TrimPrefix(c.r.Header.Get("Content-Range"), "bytes ")
	rng, total, _ := strings.Cut(cr, "/")
	c.s.mu.Lock()
	if cr == "" {
		u.data = append(u.data[:0], c.body...)
		u.total = int64(len(u.data))
	} else {
		if n, err := strconv.ParseInt(total, 10, 64); err == nil {
			u.total = n
		}
		if rng != "*" {
			a, b, _ := strings.Cut(rng, "-")
			if from, err := strconv.ParseInt(a, 10, 64); err == nil && from <= int64(len(u.data)) {
				u.data = append(u.data[:from], c.body...)
			}
			if b == "*" {
				// "bytes 0-*/*": the rest of the media streamed in this one request (Node).
				u.total = int64(len(u.data))
			}
		} else {
			c.e.Op = "gcs.upload.query"
		}
	}
	complete := u.total >= 0 && int64(len(u.data)) == u.total && u.done == nil
	got := len(u.data)
	c.s.mu.Unlock()
	if complete {
		u.done = c.s.put(u.bucket, u.name, metadataBody{ContentType: u.contentType, Metadata: u.custom}, u.data)
	}
	if u.done != nil {
		c.json(http.StatusOK, c.gcsResource(u.done))
		return
	}
	if got > 0 {
		c.w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", got-1))
	}
	if c.r.Header.Get("X-GUploader-No-308") == "yes" {
		// Go's client asks for 200 plus this header: its http.Client would treat a 308 as a redirect.
		setHeader(c.w.Header(), "X-HTTP-Status-Code-Override", "308")
		c.w.WriteHeader(http.StatusOK)
		return
	}
	c.w.WriteHeader(308)
}

// xmlRead answers GET/HEAD /{bucket}/{name} for a bucket the stub knows (Go's default reads).
func (c *call) xmlRead(rest string) {
	b, n, found := strings.Cut(rest, "/")
	bucket, err1 := url.PathUnescape(b)
	name, err2 := url.PathUnescape(n)
	c.s.mu.Lock()
	known := c.s.knownBuckets[bucket] || c.s.objects[bucket] != nil
	c.s.mu.Unlock()
	if !found || err1 != nil || err2 != nil || !known || (c.r.Method != http.MethodGet && c.r.Method != http.MethodHead) {
		c.fail(http.StatusNotFound, "unrouted", "the stub does not serve this path")
		return
	}
	c.media("xml.media", bucket, name)
}

func (s *server) newUpload(u *upload) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("stub-upload-%d-%d", time.Now().UnixNano(), s.seq)
	s.uploads[id] = u
	return id
}

func (s *server) get(bucket, name string) *object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[bucket][name]
}

func (s *server) put(bucket, name string, md metadataBody, data []byte) *object {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	now := time.Now().UTC()
	ct := md.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	o := &object{
		bucket: bucket, name: name, contentType: ct, data: append([]byte(nil), data...),
		generation: now.UnixMicro(), metageneration: 1, custom: md.Metadata,
		token: fmt.Sprintf("stub-token-%d", s.seq), created: now, updated: now,
		cacheControl: md.CacheControl, contentDisposition: md.ContentDisposition,
	}
	if o.contentDisposition == "" {
		o.contentDisposition = "inline; filename*=utf-8''" + url.PathEscape(filepath.Base(name))
	}
	if s.objects[bucket] == nil {
		s.objects[bucket] = map[string]*object{}
	}
	s.objects[bucket][name] = o
	return o
}

func (s *server) patch(bucket, name string, body []byte) *object {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objects[bucket][name]
	if o == nil {
		return nil
	}
	md := parseMetadata(body)
	if md.ContentType != "" {
		o.contentType = md.ContentType
	}
	if md.CacheControl != "" {
		o.cacheControl = md.CacheControl
	}
	if md.Metadata != nil {
		if o.custom == nil {
			o.custom = map[string]string{}
		}
		for k, v := range md.Metadata {
			o.custom[k] = v
		}
	}
	o.metageneration++
	o.updated = time.Now().UTC()
	return o
}

// fbResource is the Firebase Storage REST metadata of an object.
func (c *call) fbResource(o *object) map[string]any {
	m := map[string]any{
		"name": o.name, "bucket": o.bucket,
		"generation": strconv.FormatInt(o.generation, 10), "metageneration": strconv.FormatInt(o.metageneration, 10),
		"contentType": o.contentType, "timeCreated": o.created.Format(time.RFC3339Nano), "updated": o.updated.Format(time.RFC3339Nano),
		"storageClass": "STANDARD", "size": strconv.Itoa(len(o.data)), "md5Hash": md5B64(o.data), "crc32c": crc32cB64(o.data),
		"etag": etag(o), "contentEncoding": "identity", "contentDisposition": o.contentDisposition,
		"downloadTokens": o.token,
	}
	if o.cacheControl != "" {
		m["cacheControl"] = o.cacheControl
	}
	if len(o.custom) > 0 {
		m["metadata"] = o.custom
	}
	return m
}

// gcsResource is the GCS JSON API resource of an object.
func (c *call) gcsResource(o *object) map[string]any {
	gen := strconv.FormatInt(o.generation, 10)
	custom := map[string]string{"firebaseStorageDownloadTokens": o.token}
	for k, v := range o.custom {
		custom[k] = v
	}
	esc := url.PathEscape(o.name)
	return map[string]any{
		"kind": "storage#object", "id": o.bucket + "/" + o.name + "/" + gen,
		"selfLink":  c.base + "/storage/v1/b/" + o.bucket + "/o/" + esc,
		"mediaLink": c.base + "/download/storage/v1/b/" + o.bucket + "/o/" + esc + "?generation=" + gen + "&alt=media",
		"name":      o.name, "bucket": o.bucket, "generation": gen, "metageneration": strconv.FormatInt(o.metageneration, 10),
		"contentType": o.contentType, "storageClass": "STANDARD", "size": strconv.Itoa(len(o.data)),
		"md5Hash": md5B64(o.data), "crc32c": crc32cB64(o.data), "etag": etag(o),
		"timeCreated": o.created.Format(time.RFC3339Nano), "updated": o.updated.Format(time.RFC3339Nano),
		"timeStorageClassUpdated": o.created.Format(time.RFC3339Nano), "contentDisposition": o.contentDisposition,
		"metadata": custom,
	}
}

func etag(o *object) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d/%d", o.generation, o.metageneration)))
}

func md5B64(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func crc32cB64(b []byte) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	return base64.StdEncoding.EncodeToString(buf[:])
}

// guardProxy is a forward proxy that refuses everything and records what was asked: point
// HTTPS_PROXY/HTTP_PROXY of a process at it to see (and stop) every request leaving the machine.
type guardProxy struct{ s *server }

func (g guardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if r.Method != http.MethodConnect && r.URL.IsAbs() {
		target = r.URL.Scheme + "://" + r.URL.Host + r.URL.EscapedPath()
	}
	g.s.write(&entry{
		Time: time.Now().UTC().Format(time.RFC3339Nano), Listener: "guard", Method: r.Method,
		Host: r.Host, Path: target, Op: "guard.block", Auth: authSummary(r.Header.Get("Authorization")),
		Status: http.StatusForbidden,
	})
	http.Error(w, "blocked by the S0-B network guard", http.StatusForbidden)
}

// loadOrMakeCert returns the server certificate in dir, making a throwaway CA and a certificate for
// localhost, 127.0.0.1, ::1 and 10.0.2.2 (the host as an Android emulator sees it) when there is
// none. The CA's private key is never written: only ca.pem, to be trusted by the clients.
func loadOrMakeCert(dir string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil && time.Now().Before(leaf.NotAfter.Add(-time.Hour)) {
			return c, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tls.Certificate{}, err
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "Aruma S0-B throwaway CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("10.0.2.2")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	write := func(path, typ string, b []byte, mode os.FileMode) error {
		return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}), mode)
	}
	if err := write(filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := write(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := write(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// summarize prints, from one or more logs, a count of requests per operation, where they arrived
// (listener and prefix) and with what authorization.
func summarize(paths []string) error {
	type key struct{ op, where, auth string }
	counts := map[key]int{}
	var keys []key
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(f)
		for {
			var e entry
			if err := dec.Decode(&e); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				f.Close()
				return err
			}
			where := e.Listener
			if e.Prefix != "" {
				where += " /" + e.Prefix
			}
			if e.Op == "guard.block" {
				where += " " + e.Path
			}
			auth, _, _ := strings.Cut(e.Auth, "(")
			k := key{e.Op, where, auth}
			if counts[k] == 0 {
				keys = append(keys, k)
			}
			counts[k]++
		}
		f.Close()
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].op != keys[j].op {
			return keys[i].op < keys[j].op
		}
		return keys[i].where < keys[j].where
	})
	fmt.Printf("%-28s %-48s %-9s %s\n", "op", "arrived at", "auth", "count")
	for _, k := range keys {
		fmt.Printf("%-28s %-48s %-9s %d\n", k.op, k.where, k.auth, counts[k])
	}
	return nil
}
