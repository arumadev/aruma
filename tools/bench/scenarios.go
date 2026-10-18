package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Scenario is one request type the bench drives.
type Scenario struct {
	Name string
	What string // what the request exercises, for the report
	// Concurrency overrides the -c flag when positive (login is bound by argon2id).
	Concurrency int
	// Argon2 marks a scenario bound by the server's argon2id gate; -login-c sets its workers.
	Argon2 bool
	// SpreadClients sends every request from another X-Forwarded-For address (see
	// spreadAddr). The credential endpoints rate-limit per client address (login 10/min,
	// token 120/min, Auth client calls 300/min); spreading lets the bench measure the work
	// behind the limit instead of a stream of 429s. It only has effect when the server trusts
	// the bench's address as a proxy, which in-process mode configures.
	SpreadClients bool
	NeedSession   bool
	NeedSA        bool
	NeedAuth      bool
	// New builds the request function once the fixture exists.
	New func(t *Target, f *Fixture) func(ctx context.Context, worker int) (*http.Request, error)
}

var counter, addrCounter atomic.Uint64

// clientAddrs is how many addresses spreadAddr cycles through; 0 means a new one per call.
var clientAddrs uint64 = 1 << 16

// spreadAddr returns the next private address of the pool: 10.0.0.1, 10.0.0.2, … The pool is
// bounded because each limiter tracks at most 100,000 addresses and refuses newcomers when
// full: a new address per request fills that within seconds at Auth's request rates. 65,536
// addresses stay under the cap, and each one is reused too rarely to reach its own limit.
func spreadAddr() string {
	n := addrCounter.Add(1)
	if clientAddrs > 0 {
		n = (n-1)%clientAddrs + 1
	}
	return fmt.Sprintf("10.%d.%d.%d", n>>16&0xff, n>>8&0xff, n&0xff)
}

// pick cycles through a list across all workers.
func pick(list []string) string {
	return list[int(counter.Add(1)%uint64(len(list)))]
}

func get(ctx context.Context, u string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "GET", u, nil)
}

// Scenarios are every scenario, in the order they run.
var Scenarios = append(platformScenarios, authScenarios...)

// platformScenarios are the Phase 1 platform scenarios.
var platformScenarios = []Scenario{
	{
		Name: "healthz",
		What: "GET /healthz: the HTTP stack, middleware and request log, no database",
		New: func(t *Target, _ *Fixture) func(context.Context, int) (*http.Request, error) {
			u := t.URL + "/healthz"
			return func(ctx context.Context, _ int) (*http.Request, error) { return get(ctx, u) }
		},
	},
	{
		Name: "mgmt-list-projects",
		What: "GET /v1beta1/projects: CLI token HMAC lookup, then the owner's project list",
		New: func(t *Target, _ *Fixture) func(context.Context, int) (*http.Request, error) {
			u := t.URL + "/v1beta1/projects"
			return func(ctx context.Context, _ int) (*http.Request, error) {
				req, err := get(ctx, u)
				if err == nil {
					req.Header.Set("Authorization", "Bearer "+t.Token)
				}
				return req, err
			}
		},
	},
	{
		Name: "mgmt-web-config",
		What: "GET /v1beta1/projects/-/webApps/{app}/config: token, ownership from the app id, config file",
		New: func(t *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			return func(ctx context.Context, _ int) (*http.Request, error) {
				req, err := get(ctx, t.URL+"/v1beta1/projects/-/webApps/"+pick(f.WebApps)+"/config")
				if err == nil {
					req.Header.Set("Authorization", "Bearer "+t.Token)
				}
				return req, err
			}
		},
	},
	{
		Name:        "console-list-apps",
		What:        "GET /console/v1/projects/{p}/apps with a session cookie: session lookup, ownership, app list",
		NeedSession: true,
		New: func(t *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			return func(ctx context.Context, _ int) (*http.Request, error) {
				req, err := get(ctx, t.URL+"/console/v1/projects/"+pick(f.Projects)+"/apps")
				if err == nil {
					req.Header.Set("Cookie", f.Cookie)
				}
				return req, err
			}
		},
	},
	{
		Name:          "oauth-token",
		What:          "POST /oauth2/v1/token: RS256 JWT bearer grant, key lookup, signature check, token insert",
		SpreadClients: true,
		NeedSA:        true,
		New: func(t *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			return func(ctx context.Context, _ int) (*http.Request, error) {
				form := url.Values{
					"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
					"assertion":  {pick(f.Assertions)},
				}
				req, err := http.NewRequestWithContext(ctx, "POST", f.TokenURL, strings.NewReader(form.Encode()))
				if err == nil {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				return req, err
			}
		},
	},
	{
		Name:          "console-login",
		What:          "POST /console/v1/auth:login: argon2id verify (m=19 MiB, t=2, at most 4 at once), session insert",
		Concurrency:   4,
		Argon2:        true,
		SpreadClients: true,
		New: func(t *Target, _ *Fixture) func(context.Context, int) (*http.Request, error) {
			body, _ := json.Marshal(map[string]string{"email": t.Email, "password": t.Password})
			return func(ctx context.Context, _ int) (*http.Request, error) {
				req, err := http.NewRequestWithContext(ctx, "POST", t.URL+"/console/v1/auth:login", bytes.NewReader(body))
				if err == nil {
					req.Header.Set("Content-Type", "application/json")
				}
				return req, err
			}
		},
	},
}

// requester turns a scenario's request builder into the driver's function: it adds the
// spread address, sends, and drains the body so the connection is reused.
func requester(hc *http.Client, sc Scenario, build func(context.Context, int) (*http.Request, error)) func(context.Context, int) Outcome {
	return func(ctx context.Context, w int) Outcome {
		req, err := build(ctx, w)
		if err != nil {
			return Outcome{Err: err}
		}
		req.Header.Set("User-Agent", "aruma-bench")
		if sc.SpreadClients {
			req.Header.Set("X-Forwarded-For", spreadAddr())
		}
		resp, err := hc.Do(req)
		if err != nil {
			return Outcome{Err: err}
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			return Outcome{Err: err}
		}
		return Outcome{Status: resp.StatusCode}
	}
}
