// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// The client against a live Aruma server, next to the official firebase-admin-go app made from
// the key file Aruma issued: AC-A5, AC-A10 to AC-A14 and AC-A20. test/smoke/genconfig -serve
// starts the server on a fresh database (Go and the local Postgres are needed); ARUMA_URL with
// ARUMA_TEST_DIR uses a running one instead. Nothing may connect beyond loopback.

var live struct {
	url, dir string
	expected struct {
		ProjectID string `json:"projectId"`
		Web       struct {
			APIKey string `json:"apiKey"`
		} `json:"web"`
	}
}

var (
	dialMu  sync.Mutex
	remotes []string
)

func TestMain(m *testing.M) {
	t := http.DefaultTransport.(*http.Transport)
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			dialMu.Lock()
			remotes = append(remotes, addr)
			dialMu.Unlock()
			return nil, errors.New("network access beyond loopback blocked by the test")
		}
		return dial(ctx, network, addr)
	}
	stop, err := startAruma()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Aruma:", err)
		os.Exit(1)
	}
	code := m.Run()
	stop()
	if len(remotes) > 0 {
		fmt.Fprintln(os.Stderr, "network access attempted:", remotes)
		code = 1
	}
	os.Exit(code)
}

func startAruma() (func(), error) {
	live.url, live.dir = os.Getenv("ARUMA_URL"), os.Getenv("ARUMA_TEST_DIR")
	stop := func() {}
	if live.url == "" || live.dir == "" {
		root, err := filepath.Abs("../../..")
		if err != nil {
			return nil, err
		}
		bin := filepath.Join(root, ".cache", "bin", "genconfig")
		build := exec.Command("go", "build", "-o", bin, "./test/smoke/genconfig")
		build.Dir, build.Stdout, build.Stderr = root, os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return nil, err
		}
		if live.dir, err = os.MkdirTemp("", "aruma-adminauth-"); err != nil {
			return nil, err
		}
		cmd := exec.Command(bin, "-out", live.dir, "-serve")
		cmd.Dir, cmd.Stderr = root, os.Stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		stop = func() {
			_ = cmd.Process.Signal(syscall.SIGTERM) // genconfig drops its database on the way out
			_ = cmd.Wait()
			_ = os.RemoveAll(live.dir)
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "export ARUMA_URL="); ok {
				live.url = v
			}
			if strings.HasPrefix(sc.Text(), "export ARUMA_CLI_TOKEN=") {
				break
			}
		}
		if live.url == "" {
			stop()
			return nil, errors.New("genconfig printed no URL")
		}
	}
	b, err := os.ReadFile(filepath.Join(live.dir, "expected.json"))
	if err != nil {
		stop()
		return nil, err
	}
	return stop, json.Unmarshal(b, &live.expected)
}

// setup returns the official app made from the key file, and the Aruma client: the one line.
func setup(t *testing.T) (*firebase.App, *Client) {
	t.Helper()
	ctx := context.Background()
	keyFile := filepath.Join(live.dir, "service-account.json")
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ctx, app, WithCredentialsFile(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	return app, client
}

// clientCall makes a client SDK call with the project's API key.
func clientCall(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(live.url+path+"?key="+live.expected.Web.APIKey, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

type session struct{ uid, idToken, refreshToken string }

func signUp(t *testing.T, email string) session {
	t.Helper()
	status, out := clientCall(t, "/identitytoolkit.googleapis.com/v1/accounts:signUp", map[string]any{"email": email, "password": "secret1", "returnSecureToken": true})
	if status != 200 {
		t.Fatalf("signUp: %d %v", status, out)
	}
	return session{out["localId"].(string), out["idToken"].(string), out["refreshToken"].(string)}
}

// refresh returns the new ID token, or the error code.
func refresh(t *testing.T, refreshToken string) (string, string) {
	t.Helper()
	_, out := clientCall(t, "/securetoken.googleapis.com/v1/token", map[string]any{"grantType": "refresh_token", "refreshToken": refreshToken})
	if e, ok := out["error"].(map[string]any); ok {
		return "", e["message"].(string)
	}
	return out["id_token"].(string), ""
}

// nextSecond waits for a new second: validSince has one-second resolution, as in Firebase.
func nextSecond() {
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 10*time.Millisecond)))
}

// AC-A10
func TestLiveVerifyIDToken(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	s := signUp(t, "go-verify@example.com")
	tok, err := c.VerifyIDTokenAndCheckRevoked(ctx, s.idToken)
	if err != nil {
		t.Fatal(err)
	}
	if tok.UID != s.uid || tok.Audience != live.expected.ProjectID || tok.Issuer != live.url+"/issuers/"+live.expected.ProjectID ||
		tok.Firebase.SignInProvider != "password" || tok.Claims["email"] != "go-verify@example.com" || tok.AuthTime == 0 {
		t.Fatalf("token: %+v", tok)
	}
	if _, err := c.VerifyIDToken(ctx, s.idToken[:len(s.idToken)-3]+"AAA"); !auth.IsIDTokenInvalid(err) {
		t.Fatalf("tampered token: %v", err)
	}
}

// AC-A5
func TestLiveOfficialCustomToken(t *testing.T) {
	app, c := setup(t)
	ctx := context.Background()
	official, err := app.Auth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := official.CustomTokenWithClaims(ctx, "go-custom", map[string]interface{}{"plan": "pro"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.CustomTokenWithClaims(ctx, "go-custom", map[string]interface{}{"plan": "pro"})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{a, b} {
		status, out := clientCall(t, "/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken", map[string]any{"token": token, "returnSecureToken": true})
		if status != 200 {
			t.Fatalf("signInWithCustomToken: %d %v", status, out)
		}
		tok, err := c.VerifyIDTokenAndCheckRevoked(ctx, out["idToken"].(string))
		if err != nil || tok.UID != "go-custom" || tok.Claims["plan"] != "pro" || tok.Firebase.SignInProvider != "custom" {
			t.Fatalf("custom sign-in token: %+v %v", tok, err)
		}
	}
}

// AC-A11
func TestLiveRevoke(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	s := signUp(t, "go-revoke@example.com")
	nextSecond()
	if err := c.RevokeRefreshTokens(ctx, s.uid); err != nil {
		t.Fatal(err)
	}
	if _, code := refresh(t, s.refreshToken); code != "TOKEN_EXPIRED" {
		t.Fatalf("refresh after revoke: %q", code)
	}
	if _, err := c.VerifyIDToken(ctx, s.idToken); err != nil {
		t.Fatalf("the signature stays valid: %v", err)
	}
	if _, err := c.VerifyIDTokenAndCheckRevoked(ctx, s.idToken); !auth.IsIDTokenRevoked(err) {
		t.Fatalf("want ID_TOKEN_REVOKED, got %v", err)
	}
}

// AC-A12
func TestLiveDisable(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	s := signUp(t, "go-disable@example.com")
	u, err := c.UpdateUser(ctx, s.uid, (&auth.UserToUpdate{}).Disabled(true))
	if err != nil || !u.Disabled {
		t.Fatalf("disable: %+v %v", u, err)
	}
	if _, err := c.VerifyIDTokenAndCheckRevoked(ctx, s.idToken); !auth.IsUserDisabled(err) {
		t.Fatalf("want USER_DISABLED, got %v", err)
	}
	if _, code := refresh(t, s.refreshToken); code != "USER_DISABLED" {
		t.Fatalf("refresh while disabled: %q", code)
	}
	if _, err := c.UpdateUser(ctx, s.uid, (&auth.UserToUpdate{}).Disabled(false)); err != nil {
		t.Fatal(err)
	}
	if _, code := refresh(t, s.refreshToken); code != "" {
		t.Fatalf("refresh after enabling: %q", code)
	}
}

// AC-A13
func TestLiveCustomClaims(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	s := signUp(t, "go-claims@example.com")
	if err := c.SetCustomUserClaims(ctx, s.uid, map[string]interface{}{"role": "admin"}); err != nil {
		t.Fatal(err)
	}
	if tok, _ := c.VerifyIDToken(ctx, s.idToken); tok.Claims["role"] != nil {
		t.Fatal("claims in the old token")
	}
	fresh, code := refresh(t, s.refreshToken)
	if code != "" {
		t.Fatal(code)
	}
	if tok, err := c.VerifyIDToken(ctx, fresh); err != nil || tok.Claims["role"] != "admin" {
		t.Fatalf("claims after refresh: %+v %v", tok, err)
	}
	if err := c.SetCustomUserClaims(ctx, s.uid, map[string]interface{}{"sub": "x"}); err == nil {
		t.Fatal("reserved claim accepted")
	}
	if err := c.SetCustomUserClaims(ctx, s.uid, map[string]interface{}{"big": strings.Repeat("x", 1000)}); err == nil {
		t.Fatal("oversized claims accepted")
	}
	if err := c.SetCustomUserClaims(ctx, s.uid, nil); err != nil {
		t.Fatal(err)
	}
	if u, _ := c.GetUser(ctx, s.uid); u.CustomClaims != nil {
		t.Fatalf("claims after clearing: %v", u.CustomClaims)
	}
}

// AC-A14
func TestLiveUserManagement(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	u, err := c.CreateUser(ctx, (&auth.UserToCreate{}).UID("go-made").Email("GoMade@Example.com").Password("secret1").DisplayName("سارة").PhotoURL("https://example.com/p.png"))
	if err != nil {
		t.Fatal(err)
	}
	if u.UID != "go-made" || u.Email != "GoMade@Example.com" || u.DisplayName != "سارة" || u.ProviderID != "firebase" ||
		len(u.ProviderUserInfo) != 1 || u.ProviderUserInfo[0].ProviderID != "password" || u.UserMetadata.CreationTimestamp == 0 || u.TokensValidAfterMillis == 0 {
		t.Fatalf("record: %+v", u)
	}
	if _, err := c.CreateUser(ctx, (&auth.UserToCreate{}).Email("gomade@example.com")); !auth.IsEmailAlreadyExists(err) {
		t.Fatalf("duplicate email: %v", err)
	}
	if _, err := c.CreateUser(ctx, (&auth.UserToCreate{}).UID("go-made")); !auth.IsUIDAlreadyExists(err) {
		t.Fatalf("duplicate uid: %v", err)
	}
	if _, err := c.GetUser(ctx, "ghost"); !auth.IsUserNotFound(err) {
		t.Fatalf("unknown uid: %v", err)
	}
	if _, err := c.GetUserByEmail(ctx, "ghost@example.com"); !auth.IsUserNotFound(err) {
		t.Fatalf("unknown email: %v", err)
	}
	if got, err := c.GetUserByEmail(ctx, "gomade@example.com"); err != nil || got.UID != "go-made" {
		t.Fatalf("by email: %v %v", got, err)
	}
	res, err := c.GetUsers(ctx, []auth.UserIdentifier{auth.UIDIdentifier{UID: "go-made"}, auth.EmailIdentifier{Email: "GOMADE@example.com"}, auth.UIDIdentifier{UID: "ghost"}})
	if err != nil || len(res.Users) != 1 || len(res.NotFound) != 1 || res.NotFound[0] != (auth.UIDIdentifier{UID: "ghost"}) {
		t.Fatalf("GetUsers: %+v %v", res, err)
	}
	u, err = c.UpdateUser(ctx, "go-made", (&auth.UserToUpdate{}).DisplayName("").EmailVerified(true))
	if err != nil || u.DisplayName != "" || !u.EmailVerified {
		t.Fatalf("update: %+v %v", u, err)
	}
	if _, err := c.UpdateUser(ctx, "ghost", (&auth.UserToUpdate{}).DisplayName("x")); !auth.IsUserNotFound(err) {
		t.Fatalf("update unknown: %v", err)
	}

	for i := range 3 {
		if _, err := c.CreateUser(ctx, (&auth.UserToCreate{}).UID(fmt.Sprintf("go-page-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	it := c.Users(ctx, "")
	it.PageInfo().MaxSize = 2
	for {
		u, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, u.UID)
	}
	for _, want := range []string{"go-made", "go-page-0", "go-page-1", "go-page-2"} {
		if !slices.Contains(seen, want) {
			t.Fatalf("%s not listed: %v", want, seen)
		}
	}
	compact := slices.Clone(seen)
	slices.Sort(compact)
	if len(slices.Compact(compact)) != len(seen) {
		t.Fatalf("a user was listed twice: %v", seen)
	}

	del, err := c.DeleteUsers(ctx, []string{"go-page-0", "go-page-1", "ghost"})
	if err != nil || del.SuccessCount != 3 || del.FailureCount != 0 {
		t.Fatalf("DeleteUsers: %+v %v", del, err)
	}
	if err := c.DeleteUser(ctx, "go-page-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteUser(ctx, "go-page-2"); !auth.IsUserNotFound(err) {
		t.Fatalf("second delete: %v", err)
	}
}

// AC-A20
func TestLiveSessionCookie(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	s := signUp(t, "go-cookie@example.com")
	cookie, err := c.SessionCookie(ctx, s.idToken, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.VerifySessionCookieAndCheckRevoked(ctx, cookie)
	if err != nil || tok.UID != s.uid || tok.Issuer != live.url+"/session-issuers/"+live.expected.ProjectID || tok.Expires-tok.IssuedAt != 3600 {
		t.Fatalf("cookie: %+v %v", tok, err)
	}
	if _, err := c.VerifyIDToken(ctx, cookie); !auth.IsIDTokenInvalid(err) {
		t.Fatalf("cookie as ID token: %v", err)
	}
	if _, err := c.VerifySessionCookie(ctx, s.idToken); !auth.IsSessionCookieInvalid(err) {
		t.Fatalf("ID token as cookie: %v", err)
	}
	for _, d := range []time.Duration{time.Minute, 15 * 24 * time.Hour} {
		if _, err := c.SessionCookie(ctx, s.idToken, d); err == nil {
			t.Fatalf("duration %v accepted", d)
		}
	}
	nextSecond()
	if err := c.RevokeRefreshTokens(ctx, s.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VerifySessionCookieAndCheckRevoked(ctx, cookie); !auth.IsSessionCookieRevoked(err) {
		t.Fatalf("want SESSION_COOKIE_REVOKED, got %v", err)
	}
}
