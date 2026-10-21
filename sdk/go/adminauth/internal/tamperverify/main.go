// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Command tamperverify puts ID tokens to this library for the repository's tamper suite
// (test/acceptance/auth_tamper_test.go). It reads one JSON request per line on stdin,
//
//	{"url": "<Aruma server>", "key": "<service account key file JSON>", "token": "…", "checkRevoked": true}
//
// and answers one line per request: {"ok": true} or {"ok": false, "error": "…"}. Clients are
// kept per server and key, as an application keeps its one client.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"

	"github.com/arumadev/aruma/sdk/go/adminauth"
)

type request struct {
	URL          string `json:"url"`
	Key          string `json:"key"`
	Token        string `json:"token"`
	CheckRevoked bool   `json:"checkRevoked"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func main() {
	ctx := context.Background()
	clients := map[string]*adminauth.Client{}
	client := func(req request) (*adminauth.Client, error) {
		id := req.URL + "|" + req.Key
		if c, ok := clients[id]; ok {
			return c, nil
		}
		var key struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal([]byte(req.Key), &key); err != nil {
			return nil, err
		}
		app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: key.ProjectID}, option.WithCredentialsJSON([]byte(req.Key)))
		if err != nil {
			return nil, err
		}
		c, err := adminauth.NewClient(ctx, app, adminauth.WithCredentialsJSON([]byte(req.Key)), adminauth.WithURL(req.URL))
		if err != nil {
			return nil, err
		}
		clients[id] = c
		return c, nil
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req request
		err := json.Unmarshal(in.Bytes(), &req)
		var c *adminauth.Client
		if err == nil {
			c, err = client(req)
		}
		if err == nil {
			if req.CheckRevoked {
				_, err = c.VerifyIDTokenAndCheckRevoked(ctx, req.Token)
			} else {
				_, err = c.VerifyIDToken(ctx, req.Token)
			}
		}
		res := response{OK: err == nil}
		if err != nil {
			res.Error = err.Error()
		}
		if err := out.Encode(res); err != nil {
			os.Exit(1)
		}
	}
}
