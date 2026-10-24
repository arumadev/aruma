// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminstorage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
)

// fakeAruma answers the token endpoint and the Firebase metadata of three objects: one with two
// tokens, one with none, and a missing one. The live server is the smoke test's
// (test/smoke/admin-go/storage).
func fakeAruma(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var exchanges atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/v1/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != jwtBearer || r.FormValue("assertion") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		exchanges.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aruma-token", "expires_in": 3600, "token_type": "Bearer"})
	})
	mux.HandleFunc("GET /v0/b/{bucket}/o/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer aruma-token" {
			http.Error(w, `{"error":{"code":401,"message":"no token"}}`, http.StatusUnauthorized)
			return
		}
		switch r.PathValue("name") {
		case "photos/a b.png":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "photos/a b.png", "downloadTokens": "tok-1,tok-2"})
		case "bare.txt":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "bare.txt"})
		default:
			http.Error(w, `{"error":{"code":404,"message":"Not Found."}}`, http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &exchanges
}

func TestDownloadURL(t *testing.T) {
	srv, exchanges := fakeAruma(t)
	// universe_domain in the key changes nothing: the token comes from token_uri all the same.
	for _, universe := range []string{"", "googleapis.com", "127.0.0.1"} {
		t.Run("universe_domain="+universe, func(t *testing.T) {
			extra := map[string]string{}
			if universe != "" {
				extra["universe_domain"] = universe
			}
			c, err := NewClient(t.Context(), WithCredentialsJSON(keyJSON(t, srv.URL+"/oauth2/v1/token", extra)))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			b := c.Storage().Bucket("demo-app.aruma.test")
			got, err := c.DownloadURL(t.Context(), b.Object("photos/a b.png"))
			want := srv.URL + "/v0/b/demo-app.aruma.test/o/photos%2Fa%20b.png?alt=media&token=tok-1"
			if err != nil || got != want {
				t.Errorf("DownloadURL = %q, %v; want %q", got, err, want)
			}
			if _, err := c.DownloadURL(t.Context(), b.Object("bare.txt")); !errors.Is(err, ErrNoDownloadToken) {
				t.Errorf("no token: %v", err)
			}
			if _, err := c.DownloadURL(t.Context(), b.Object("missing")); !errors.Is(err, storage.ErrObjectNotExist) {
				t.Errorf("missing object: %v", err)
			}
			if _, err := c.DownloadURL(t.Context(), b.Object("")); err == nil {
				t.Error("an empty name was accepted")
			}
		})
	}
	// One exchange per client: the token is kept until shortly before it expires.
	if n := exchanges.Load(); n != 3 {
		t.Errorf("%d token exchanges, want 3", n)
	}
}

func TestRefusedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"unknown key"}`))
	}))
	defer srv.Close()
	c, err := NewClient(t.Context(), WithCredentialsJSON(keyJSON(t, srv.URL+"/oauth2/v1/token", nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DownloadURL(t.Context(), c.Storage().Bucket("b").Object("o"))
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("got %v, want the server's reason", err)
	}
}
