package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// credentials are what `arumactl login` stores.
type credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func credentialsPath(env func(string) string) (string, error) {
	if p := env("ARUMACTL_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aruma", "credentials.json"), nil
}

// loadCredentials reads the stored login; ARUMA_SERVER and ARUMA_TOKEN override it.
func loadCredentials(env func(string) string) (credentials, error) {
	var c credentials
	if p, err := credentialsPath(env); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			if err := json.Unmarshal(b, &c); err != nil {
				return c, fmt.Errorf("%s: %w", p, err)
			}
		}
	}
	if v := env("ARUMA_SERVER"); v != "" {
		c.Server = v
	}
	if v := env("ARUMA_TOKEN"); v != "" {
		c.Token = v
	}
	if c.Server == "" || c.Token == "" {
		return c, errors.New("not logged in: run `arumactl login --server <url>` with a CLI token from the console (Account → CLI tokens)")
	}
	return c, nil
}

func saveCredentials(env func(string) string, c credentials) (string, error) {
	p, err := credentialsPath(env)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // MkdirAll leaves an existing directory as is
		return "", err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return p, writePrivate(p, append(b, '\n'))
}

// writePrivate writes a secret file readable by its owner only. It writes a fresh temporary
// file and renames it into place, so neither an existing file's mode nor a symlink planted at
// path is inherited.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".arumactl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// client calls the console API with a CLI token.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(c credentials) *client {
	return &client{base: strings.TrimSuffix(c.Server, "/"), token: c.Token, http: &http.Client{Timeout: 60 * time.Second}}
}

// apiError is a google.rpc.Status returned by the server.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (e *apiError) Error() string { return e.Message }

// response is a raw reply, for downloads.
type response struct {
	body     []byte
	filename string
}

func (c *client) do(method, path string, query url.Values, in any) (*response, error) {
	u := c.base + "/console/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error.Message != "" {
			return nil, &env.Error
		}
		return nil, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	out := &response{body: b}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		out.filename = filepath.Base(params["filename"])
	}
	return out, nil
}

// call decodes a JSON reply into out (when non-nil).
func (c *client) call(method, path string, query url.Values, in, out any) error {
	r, err := c.do(method, path, query, in)
	if err != nil {
		return err
	}
	if out == nil || len(r.body) == 0 {
		return nil
	}
	return json.Unmarshal(r.body, out)
}
