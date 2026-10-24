// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
)

// ErrNoDownloadToken is returned by DownloadURL when the object has no download token, the
// condition firebase-admin reports as storage/no-download-token.
var ErrNoDownloadToken = errors.New("adminstorage: no download token available")

// DownloadURL returns the object's download URL in the one form every platform resolves
// (ADR-022 §16): <aruma>/v0/b/<bucket>/o/<name escaped>?alt=media&token=<token>, the Aruma URL
// in lower case without a default port. Like firebase-admin's getDownloadURL, it reads the
// Firebase metadata of the object with the service account, here from Aruma
// (GET /v0/b/<bucket>/o/<name>), which gives an object without a token its first one, and
// takes the first token. A missing object is storage.ErrObjectNotExist.
func (c *Client) DownloadURL(ctx context.Context, obj *storage.ObjectHandle) (string, error) {
	if obj == nil || obj.BucketName() == "" || obj.ObjectName() == "" {
		return "", errors.New("adminstorage: DownloadURL needs an object handle with a bucket and a name")
	}
	u := c.url + "/v0/b/" + obj.BucketName() + "/o/" + escapeComponent(obj.ObjectName())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("adminstorage: read the metadata of %s: %w", obj.ObjectName(), err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("adminstorage: %s: %w", obj.ObjectName(), storage.ErrObjectNotExist)
	case resp.StatusCode != http.StatusOK:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return "", fmt.Errorf("adminstorage: read the metadata of %s: HTTP %d %s", obj.ObjectName(), resp.StatusCode, e.Error.Message)
	}
	var md struct {
		DownloadTokens string `json:"downloadTokens"`
	}
	if err := json.Unmarshal(b, &md); err != nil {
		return "", fmt.Errorf("adminstorage: parse the metadata of %s: %w", obj.ObjectName(), err)
	}
	token, _, _ := strings.Cut(md.DownloadTokens, ",")
	if token == "" {
		return "", ErrNoDownloadToken
	}
	return u + "?alt=media&token=" + token, nil
}

// escapeComponent escapes s as JavaScript's encodeURIComponent does, which is how every
// Firebase SDK writes the object name into the URL: everything but A-Z a-z 0-9 - _ . ! ~ * ' ( )
// is percent-encoded byte by byte, "/" included.
func escapeComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
