// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminstorage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// Client is Aruma Cloud Storage for a server: the methods of firebase-admin-go's storage.Client
// on a cloud.google.com/go/storage client that reaches Aruma, and DownloadURL.
type Client struct {
	gcs    *storage.Client
	http   *http.Client // carries the service account's token, to Aruma only
	url    string
	bucket string
}

// Option configures NewClient.
type Option func(*settings)

type settings struct {
	url        string
	keyJSON    []byte
	keyFile    string
	bucket     string
	httpClient *http.Client
}

// WithURL sets the Aruma server's public URL, such as https://aruma.example.com. The default is
// ARUMA_URL, then the origin of the key file's token_uri.
func WithURL(u string) Option { return func(s *settings) { s.url = u } }

// WithCredentialsFile reads the service account key file Aruma issued. The default is
// GOOGLE_APPLICATION_CREDENTIALS.
func WithCredentialsFile(path string) Option { return func(s *settings) { s.keyFile = path } }

// WithCredentialsJSON takes the service account key file's contents.
func WithCredentialsJSON(b []byte) Option { return func(s *settings) { s.keyJSON = b } }

// WithDefaultBucket names the bucket DefaultBucket returns: the storageBucket of the project's
// configuration, as firebase.Config.StorageBucket does for app.Storage.
func WithDefaultBucket(name string) Option { return func(s *settings) { s.bucket = name } }

// WithHTTPClient sets the HTTP client the requests to Aruma go through. Its Transport (or
// http.DefaultTransport) carries them, under the layer that adds the service account's token;
// redirects are never followed, so the token cannot be carried to another origin.
func WithHTTPClient(c *http.Client) Option { return func(s *settings) { s.httpClient = c } }

// serviceAccountKey holds the fields of a key file the library uses. universe_domain is not
// one of them: with a value other than googleapis.com, the Google libraries would sign their
// own token instead of asking token_uri for one, and Aruma accepts only the tokens it issued.
type serviceAccountKey struct {
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// NewClient returns Aruma Cloud Storage for the service account in the key file. The client
// sends every request, the resumable uploads' chunks included, to the Aruma URL with the
// account's access token, which it gets from Aruma's token endpoint and refreshes itself.
func NewClient(ctx context.Context, opts ...Option) (*Client, error) {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	if err := emulatorGuard(os.Getenv); err != nil {
		return nil, err
	}
	key, err := loadKey(s, os.Getenv)
	if err != nil {
		return nil, err
	}
	base, err := resolveURL(s.url, key, os.Getenv)
	if err != nil {
		return nil, err
	}
	rt := baseTransport(s.httpClient)
	ts, err := newTokenSource(base, key, rt)
	if err != nil {
		return nil, err
	}
	hc := authorizedClient(base, ts, s.httpClient, rt)
	// WithoutAuthentication keeps the library from looking for default credentials (a key
	// file, gcloud's, the metadata server): the client given already authorizes, and only it.
	gcs, err := storage.NewClient(ctx, option.WithEndpoint(base+"/storage/v1/"), option.WithoutAuthentication(),
		option.WithHTTPClient(hc), storage.WithJSONReads())
	if err != nil {
		return nil, fmt.Errorf("adminstorage: %w", err)
	}
	return &Client{gcs: gcs, http: hc, url: base, bucket: s.bucket}, nil
}

// emulatorGuard refuses to start with an emulator variable set: with STORAGE_EMULATOR_HOST,
// cloud.google.com/go/storage sends no credential at all, and Aruma accepts no unauthenticated
// GCS request (ADR-022 §14). firebase-admin-go turns FIREBASE_STORAGE_EMULATOR_HOST into it.
func emulatorGuard(getenv func(string) string) error {
	for _, name := range []string{"STORAGE_EMULATOR_HOST", "FIREBASE_STORAGE_EMULATOR_HOST"} {
		if getenv(name) != "" {
			return fmt.Errorf("adminstorage: %s is set: cloud.google.com/go/storage would send no credential and talk "+
				"to that host instead; unset it to reach Aruma", name)
		}
	}
	return nil
}

func loadKey(s settings, getenv func(string) string) (serviceAccountKey, error) {
	var k serviceAccountKey
	raw := s.keyJSON
	if raw == nil {
		path := cmp.Or(s.keyFile, getenv("GOOGLE_APPLICATION_CREDENTIALS"))
		if path == "" {
			return k, errors.New("adminstorage: no service account key: use WithCredentialsFile or set GOOGLE_APPLICATION_CREDENTIALS")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return k, fmt.Errorf("adminstorage: read the service account key: %w", err)
		}
		raw = b
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("adminstorage: parse the service account key: %w", err)
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return k, errors.New("adminstorage: the service account key needs client_email and private_key")
	}
	return k, nil
}

const tokenPath = "/oauth2/v1/token"

// resolveURL picks the Aruma public URL: the option, ARUMA_URL, then the key file's token_uri.
// It must be https, except on loopback, never a Google host (that would mean a Google key),
// and an origin alone: Aruma serves Storage at the root of its domain, the client uploads to
// /upload/storage/v1 from the root whatever the endpoint's path, and the download URL is
// <origin>/v0/…. The result is lower case without a default port, the form of every
// platform's download URL (ADR-022 §16).
func resolveURL(opt string, key serviceAccountKey, getenv func(string) string) (string, error) {
	raw := cmp.Or(opt, getenv("ARUMA_URL"))
	if raw == "" {
		if r, ok := strings.CutSuffix(key.TokenURI, tokenPath); ok {
			raw = r
		}
	}
	if raw == "" {
		return "", errors.New("adminstorage: set ARUMA_URL (or WithURL) to your Aruma server, for example https://aruma.example.com")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("adminstorage: invalid Aruma URL %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "google.com" || host == "googleapis.com" || strings.HasSuffix(host, ".google.com") || strings.HasSuffix(host, ".googleapis.com") {
		return "", fmt.Errorf("adminstorage: %s is a Google address, not an Aruma server: check the key file and ARUMA_URL", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(host)) {
		return "", fmt.Errorf("adminstorage: the Aruma URL must use https (plain http is allowed on loopback only): %s", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.ForceQuery {
		return "", fmt.Errorf("adminstorage: the Aruma URL must not carry a query, fragment or credentials: %s", raw)
	}
	if strings.TrimRight(u.EscapedPath(), "/") != "" {
		return "", fmt.Errorf("adminstorage: the Aruma URL must be the server's origin, without a path: %s", raw)
	}
	return origin(u), nil
}

// origin is scheme://host[:port] in lower case, without the scheme's default port.
func origin(u *url.URL) string {
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Bucket returns a handle to the named bucket, as firebase-admin-go's storage.Client.Bucket.
func (c *Client) Bucket(name string) (*storage.BucketHandle, error) {
	if name == "" {
		return nil, errors.New("bucket name not specified")
	}
	return c.gcs.Bucket(name), nil
}

// DefaultBucket returns a handle to the bucket named by WithDefaultBucket, as
// firebase-admin-go's storage.Client.DefaultBucket returns firebase.Config.StorageBucket.
func (c *Client) DefaultBucket() (*storage.BucketHandle, error) {
	return c.Bucket(c.bucket)
}

// ErrSignedURLsUnsupported is what SignedURL returns: Aruma v0.3 serves no signed URLs
// (ADR-023), and DownloadURL gives the link the Firebase SDKs use instead.
var ErrSignedURLsUnsupported = errors.New("adminstorage: signed URLs are not supported in Aruma v0.3: use DownloadURL")

// SignedURL refuses with ErrSignedURLsUnsupported. Do not call BucketHandle.SignedURL or
// GenerateSignedPostPolicyV4 on the handles either: Aruma does not verify their signatures, and
// without SignedURLOptions.GoogleAccessID the official client asks the GCE metadata server for
// the signer.
func (c *Client) SignedURL(bucket, object string, opts *storage.SignedURLOptions) (string, error) {
	return "", ErrSignedURLsUnsupported
}

// Storage is the cloud.google.com/go/storage client underneath, already pointed at Aruma.
func (c *Client) Storage() *storage.Client { return c.gcs }

// URL is the Aruma server's public URL the client talks to.
func (c *Client) URL() string { return c.url }

// Close closes the underlying client's idle connections.
func (c *Client) Close() error { return c.gcs.Close() }
