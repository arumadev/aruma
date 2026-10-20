// Command adapter lets the Firebase SDKs' own Auth integration tests, written for the Auth
// emulator, run against a throwaway Aruma server. It is test-only: it never ships with Aruma
// and must never become part of the server, which deliberately serves none of the emulator's
// helper endpoints.
//
// It sits where the tests expect the emulator (FIREBASE_AUTH_EMULATOR_HOST) and:
//
//   - forwards every Auth call to Aruma unchanged, except for four substitutions the tests'
//     fixed credentials need: any API key becomes the project's real web key, the emulator's
//     "Bearer owner" admin credential becomes the owner's CLI token, an unsigned custom token
//     (a JSON object or an "alg": "none" JWT, which only the emulator accepts) is signed with a
//     service account key of the project as createCustomToken signs it, and the client address
//     (X-Forwarded-For, which Aruma believes from this loopback proxy) changes on every reset,
//     so per-address limits apply per test instead of to the whole suite;
//   - serves the emulator-only helpers from Aruma's console and Admin APIs:
//     DELETE /emulator/v1/projects/{p}/accounts       every user is deleted, the mailbox emptied
//     GET    /emulator/v1/projects/{p}/oobCodes       the dev mailbox, oldest first
//     GET    /emulator/v1/projects/{p}/verificationCodes  always empty: Aruma sends no SMS
//     GET    /emulator/v1/projects/{p}/config         enumeration protection, in emulator terms
//     PATCH  /emulator/v1/projects/{p}/config
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

// firstClientAddr starts the synthetic client addresses, in the range reserved for
// benchmarking (RFC 2544), so they can never be a real caller's.
var firstClientAddr = netip.MustParseAddr("198.18.0.1")

// Adapter is the emulator stand-in in front of one Aruma project.
type Adapter struct {
	upstream *url.URL
	project  string  // Aruma project id; the tests' GCLOUD_PROJECT must be the same
	apiKey   string  // the project's web API key
	token    string  // the project owner's CLI token
	signer   *signer // signs the tests' unsigned custom tokens
	client   *http.Client
	proxy    *httputil.ReverseProxy

	mu   sync.Mutex
	addr netip.Addr // the client address forwarded calls carry
}

// New returns an adapter for project on the Aruma server at upstream.
func New(upstream *url.URL, project, apiKey, token string) *Adapter {
	a := &Adapter{upstream: upstream, project: project, apiKey: apiKey, token: token, client: &http.Client{}, addr: firstClientAddr}
	a.proxy = &httputil.ReverseProxy{Rewrite: a.rewrite}
	return a
}

// Handler serves the helpers and forwards everything else.
func (a *Adapter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /emulator/v1/projects/{project}/accounts", a.helper(a.reset))
	mux.HandleFunc("GET /emulator/v1/projects/{project}/oobCodes", a.helper(a.oobCodes))
	mux.HandleFunc("GET /emulator/v1/projects/{project}/verificationCodes", a.helper(a.verificationCodes))
	mux.HandleFunc("GET /emulator/v1/projects/{project}/config", a.helper(a.getConfig))
	mux.HandleFunc("PATCH /emulator/v1/projects/{project}/config", a.helper(a.patchConfig))
	mux.HandleFunc("OPTIONS /emulator/v1/", preflight)
	// The other /emulator/v1 helpers are refused; /emulator/auth/* are Aruma's own pages (ADR-014).
	mux.HandleFunc("/emulator/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "this emulator helper is not provided by the adapter")
	})
	mux.Handle("/", a.proxy)
	return mux
}

// rewrite prepares a forwarded call.
func (a *Adapter) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(a.upstream)
	pr.Out.Host = a.upstream.Host
	if q := pr.Out.URL.Query(); q.Has("key") {
		q.Set("key", a.apiKey)
		pr.Out.URL.RawQuery = q.Encode()
	}
	if pr.Out.Header.Get("X-Goog-Api-Key") != "" {
		pr.Out.Header.Set("X-Goog-Api-Key", a.apiKey)
	}
	if pr.Out.Header.Get("Authorization") == "Bearer owner" {
		pr.Out.Header.Set("Authorization", "Bearer "+a.token)
	}
	pr.Out.Header.Set("X-Forwarded-For", a.clientAddr().String())
	if pr.Out.Method == http.MethodPost && pr.Out.Body != nil && isCustomTokenSignIn(pr.Out.URL.Path) {
		if body, err := readBody(pr.Out.Body); err == nil {
			replaceBody(pr.Out, a.signCustomToken(body))
		} else {
			replaceBody(pr.Out, nil)
		}
	}
}

func (a *Adapter) clientAddr() netip.Addr {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.addr
}

func (a *Adapter) nextClientAddr() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addr = a.addr.Next()
}

// helper wraps a helper endpoint: CORS as the emulator answers it (the browser tests call the
// helpers cross-origin), and the project check.
func (a *Adapter) helper(h func(context.Context, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if p := r.PathValue("project"); p != a.project {
			writeError(w, http.StatusNotFound, fmt.Sprintf("project %q is not the one the adapter serves (%q)", p, a.project))
			return
		}
		out, err := h(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func preflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
	h.Set("Access-Control-Allow-Headers", "content-type, authorization")
	w.WriteHeader(http.StatusNoContent)
}

// reset deletes every user of the project and empties its dev mailbox, as the emulator's reset
// forgets accounts and codes, then moves to a fresh client address.
func (a *Adapter) reset(ctx context.Context, _ *http.Request) (any, error) {
	for {
		var page struct {
			Users []struct {
				LocalID string `json:"localId"`
			} `json:"users"`
		}
		if err := a.call(ctx, http.MethodGet, a.adminPath("accounts:batchGet?maxResults=1000"), nil, &page); err != nil {
			return nil, err
		}
		if len(page.Users) == 0 {
			break
		}
		ids := make([]string, len(page.Users))
		for i, u := range page.Users {
			ids[i] = u.LocalID
		}
		var res struct {
			Errors []json.RawMessage `json:"errors"`
		}
		body := map[string]any{"localIds": ids, "force": true}
		if err := a.call(ctx, http.MethodPost, a.adminPath("accounts:batchDelete"), body, &res); err != nil {
			return nil, err
		}
		if len(res.Errors) > 0 {
			return nil, fmt.Errorf("batchDelete left %d users: %s", len(res.Errors), res.Errors[0])
		}
	}
	if err := a.call(ctx, http.MethodDelete, a.consolePath("auth/mailbox"), nil, nil); err != nil {
		return nil, err
	}
	a.nextClientAddr()
	return struct{}{}, nil
}

// oobCode is one entry of the emulator's oobCodes list.
type oobCode struct {
	Email       string `json:"email"`
	RequestType string `json:"requestType"`
	OobCode     string `json:"oobCode"`
	OobLink     string `json:"oobLink"`
}

// oobCodes lists the action codes in the dev mailbox, oldest first as the emulator does (the
// mailbox lists newest first). The link is Aruma's action page link, which carries the code.
func (a *Adapter) oobCodes(ctx context.Context, _ *http.Request) (any, error) {
	var box struct {
		Messages []struct {
			To   string `json:"to"`
			Type string `json:"type"`
			Link string `json:"link"`
		} `json:"messages"`
	}
	if err := a.call(ctx, http.MethodGet, a.consolePath("auth/mailbox"), nil, &box); err != nil {
		return nil, err
	}
	codes := []oobCode{}
	for i := len(box.Messages) - 1; i >= 0; i-- {
		m := box.Messages[i]
		u, err := url.Parse(m.Link)
		if m.Link == "" || err != nil || u.Query().Get("oobCode") == "" {
			continue // a test message, without a code
		}
		codes = append(codes, oobCode{Email: m.To, RequestType: m.Type, OobCode: u.Query().Get("oobCode"), OobLink: m.Link})
	}
	return map[string]any{"oobCodes": codes}, nil
}

// verificationCodes is always empty: phone sign-in is deferred, so Aruma never sends a code.
func (a *Adapter) verificationCodes(context.Context, *http.Request) (any, error) {
	return map[string]any{"verificationCodes": []any{}}, nil
}

// emulatorConfig is the emulator's project config. Only enumeration protection exists in
// Aruma; duplicate emails and passthrough mode do not.
type emulatorConfig struct {
	SignIn struct {
		AllowDuplicateEmails bool `json:"allowDuplicateEmails"`
	} `json:"signIn"`
	UsageMode          string `json:"usageMode"`
	EmailPrivacyConfig struct {
		EnableImprovedEmailPrivacy bool `json:"enableImprovedEmailPrivacy"`
	} `json:"emailPrivacyConfig"`
}

func (a *Adapter) getConfig(ctx context.Context, _ *http.Request) (any, error) {
	var cfg struct {
		EnumerationProtection bool `json:"enumerationProtection"`
	}
	if err := a.call(ctx, http.MethodGet, a.consolePath("auth/config"), nil, &cfg); err != nil {
		return nil, err
	}
	var out emulatorConfig
	out.UsageMode = "DEFAULT"
	out.EmailPrivacyConfig.EnableImprovedEmailPrivacy = cfg.EnumerationProtection
	return out, nil
}

func (a *Adapter) patchConfig(ctx context.Context, r *http.Request) (any, error) {
	var in struct {
		SignIn *struct {
			AllowDuplicateEmails bool `json:"allowDuplicateEmails"`
		} `json:"signIn"`
		UsageMode          string `json:"usageMode"`
		EmailPrivacyConfig *struct {
			EnableImprovedEmailPrivacy bool `json:"enableImprovedEmailPrivacy"`
		} `json:"emailPrivacyConfig"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		return nil, err
	}
	switch {
	case in.SignIn != nil && in.SignIn.AllowDuplicateEmails:
		return nil, errors.New("Aruma has no duplicate-email mode")
	case in.UsageMode != "" && in.UsageMode != "DEFAULT":
		return nil, errors.New("Aruma has no passthrough mode")
	}
	if in.EmailPrivacyConfig != nil {
		body := map[string]bool{"enumerationProtection": in.EmailPrivacyConfig.EnableImprovedEmailPrivacy}
		if err := a.call(ctx, http.MethodPatch, a.consolePath("auth/config"), body, nil); err != nil {
			return nil, err
		}
	}
	return a.getConfig(ctx, r)
}

func (a *Adapter) adminPath(rest string) string {
	return "/identitytoolkit.googleapis.com/v1/projects/" + a.project + "/" + rest
}

func (a *Adapter) consolePath(rest string) string {
	return "/console/v1/projects/" + a.project + "/" + rest
}

// call sends one console or Admin API request with the owner's CLI token.
func (a *Adapter) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(a.upstream.String(), "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg}})
}
