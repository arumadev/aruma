package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The Firestore emulator, as a black box: started from its jar, driven over REST as the oracle's
// recorder drives it (test/oracle/rules/tools/record-emulator.mjs), and stopped by its pid.

const emulatorProject = "demo-rulesdiff"

type emulator struct {
	addr   string // host:port
	cmd    *exec.Cmd
	log    *os.File
	client *http.Client
	loaded string // the rules last loaded
}

// startEmulator starts the jar on port and websocket port port+1, both on 127.0.0.1, and waits
// until it answers.
func startEmulator(java, jar string, port int, logDir string) (*emulator, error) {
	if _, err := os.Stat(jar); err != nil {
		return nil, fmt.Errorf("the Firestore emulator jar: %w", err)
	}
	log, err := os.Create(filepath.Join(logDir, "firestore-emulator.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(java, "-Xmx512m", "-Duser.language=en", "-Duser.country=US", "-jar", jar,
		"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--websocket_port", strconv.Itoa(port+1))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	e := &emulator{addr: fmt.Sprintf("127.0.0.1:%d", port), cmd: cmd, log: log, client: &http.Client{Timeout: 30 * time.Second}}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := e.client.Get("http://" + e.addr + "/"); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return e, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.stop()
	// The log directory goes when the tool ends: say what the log said.
	out, _ := os.ReadFile(log.Name())
	if len(out) > 2000 {
		out = out[len(out)-2000:]
	}
	return nil, fmt.Errorf("the Firestore emulator did not answer on %s; its log ends:\n%s", e.addr, out)
}

// connectEmulator uses an emulator that is already running.
func connectEmulator(addr string) *emulator {
	return &emulator{addr: addr, client: &http.Client{Timeout: 30 * time.Second}}
}

// stop ends the emulator this tool started, by its pid: SIGTERM, then SIGKILL after ten seconds.
func (e *emulator) stop() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		e.cmd.Wait()
		close(done)
	}()
	e.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		e.cmd.Process.Kill()
		<-done
	}
	e.log.Close()
}

type response struct {
	status int
	body   []byte
}

func (e *emulator) call(ctx context.Context, method, path string, body any, token string) (response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+e.addr+path, r)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := e.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return response{status: res.StatusCode, body: b}, err
}

func errorMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(b))
}

// loadRules loads a rules file. A file the emulator does not compile is an outcome, not an
// error: its issues come back as the message.
func (e *emulator) loadRules(ctx context.Context, src string) (compiled bool, message string, err error) {
	if e.loaded == src {
		return true, "", nil
	}
	e.loaded = ""
	res, err := e.call(ctx, "PUT", "/emulator/v1/projects/"+emulatorProject+":securityRules",
		map[string]any{"rules": map[string]any{"files": []map[string]string{{"name": "firestore.rules", "content": src}}}}, "")
	if err != nil {
		return false, "", err
	}
	if res.status != http.StatusOK {
		return false, fmt.Sprintf("%d %s", res.status, errorMessage(res.body)), nil
	}
	var body struct {
		Issues []struct {
			Severity       string `json:"severity"`
			Description    string `json:"description"`
			SourcePosition struct {
				Line   int `json:"line"`
				Column int `json:"column"`
			} `json:"sourcePosition"`
		} `json:"issues"`
	}
	json.Unmarshal(res.body, &body)
	var errs []string
	for _, i := range body.Issues {
		if i.Severity == "ERROR" {
			errs = append(errs, fmt.Sprintf("L%d:%d %s", i.SourcePosition.Line, i.SourcePosition.Column, i.Description))
		}
	}
	if len(errs) > 0 {
		return false, strings.Join(errs, "\n"), nil
	}
	e.loaded = src
	return true, "", nil
}

// rest converts a JSON value to a Firestore REST value, as the recorder does.
func rest(v any) map[string]any {
	switch x := v.(type) {
	case nil:
		return map[string]any{"nullValue": nil}
	case bool:
		return map[string]any{"booleanValue": x}
	case int:
		return map[string]any{"integerValue": strconv.Itoa(x)}
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return map[string]any{"integerValue": strconv.FormatInt(int64(x), 10)}
		}
		return map[string]any{"doubleValue": x}
	case string:
		return map[string]any{"stringValue": x}
	case []any:
		vs := make([]any, len(x))
		for i, e := range x {
			vs[i] = rest(e)
		}
		return map[string]any{"arrayValue": map[string]any{"values": vs}}
	case map[string]any:
		return map[string]any{"mapValue": map[string]any{"fields": fields(x)}}
	}
	panic(fmt.Sprintf("rest: %T", v))
}

func fields(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = rest(v)
	}
	return out
}

const docsPrefix = "/v1/projects/" + emulatorProject + "/databases/(default)/documents/"

func (e *emulator) put(ctx context.Context, path string, data map[string]any) error {
	res, err := e.call(ctx, "PATCH", docsPrefix+path, map[string]any{"fields": fields(data)}, "owner")
	if err == nil && res.status != http.StatusOK {
		err = fmt.Errorf("seed %s: %d %s", path, res.status, errorMessage(res.body))
	}
	return err
}

func (e *emulator) del(ctx context.Context, path string) error {
	_, err := e.call(ctx, "DELETE", docsPrefix+path, nil, "owner")
	return err
}

// idToken returns an unsigned ID token for alice, with her claims, as the recorder builds it.
func idToken(now time.Time) string {
	b64 := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	claims := map[string]any{
		"iss": "https://securetoken.google.com/" + emulatorProject, "aud": emulatorProject,
		"iat": now.Unix(), "exp": now.Unix() + 3600, "auth_time": now.Unix(), "sub": "alice", "user_id": "alice",
	}
	for k, v := range aliceToken {
		claims[k] = v
	}
	return b64(map[string]string{"alg": "none", "typ": "JWT"}) + "." + b64(claims) + "."
}

// verdict is what one side decided.
type verdict struct {
	Compiled bool   `json:"compiled"`
	Allow    bool   `json:"allow"`
	Outcome  string `json:"outcome"` // allow, deny, error, compile-error; or skip, with Message saying why
	Message  string `json:"message,omitempty"`
}

var noMatch = regexp.MustCompile(`^No matching allow statements`)

// run seeds the case's documents, makes its request and cleans up.
func (e *emulator) run(ctx context.Context, c *genCase, src string) (verdict, error) {
	ok, msg, err := e.loadRules(ctx, src)
	if err != nil {
		return verdict{}, err
	}
	if !ok {
		return verdict{Outcome: "compile-error", Message: msg}, nil
	}
	const doc = "c/d"
	if c.Alice {
		err = e.put(ctx, "u/alice", map[string]any{"role": "editor"})
	} else {
		err = e.del(ctx, "u/alice")
	}
	if err == nil {
		if c.Existing != nil {
			err = e.put(ctx, doc, c.Existing)
		} else {
			err = e.del(ctx, doc)
		}
	}
	if err != nil {
		return verdict{}, err
	}
	token := ""
	if c.Auth {
		token = idToken(time.Now())
	}
	var res response
	switch c.Method {
	case "get":
		res, err = e.call(ctx, "GET", docsPrefix+doc, nil, token)
	case "create":
		res, err = e.call(ctx, "PATCH", docsPrefix+doc+"?currentDocument.exists=false", map[string]any{"fields": fields(c.Incoming)}, token)
	case "update":
		res, err = e.call(ctx, "PATCH", docsPrefix+doc+"?currentDocument.exists=true", map[string]any{"fields": fields(c.Incoming)}, token)
	case "delete":
		res, err = e.call(ctx, "DELETE", docsPrefix+doc, nil, token)
	default:
		return verdict{}, fmt.Errorf("method %s", c.Method)
	}
	if err != nil {
		return verdict{}, err
	}
	if err := e.del(ctx, doc); err != nil {
		return verdict{}, err
	}
	v := verdict{Compiled: true}
	m := errorMessage(res.body)
	switch {
	case res.status == http.StatusOK, res.status == http.StatusNotFound && c.Method == "get":
		v.Allow, v.Outcome = true, "allow"
	case res.status == http.StatusForbidden:
		// One reason per statement evaluated. A condition that reads resource is evaluated
		// twice and the first pass always fails (firebase-tools #6252), so any false decides.
		v.Outcome, v.Message = "error", m
		if strings.Contains(m, "false for") || noMatch.MatchString(m) {
			v.Outcome = "deny"
		}
	default:
		v.Outcome, v.Message = "skip", fmt.Sprintf("%d %s", res.status, m)
	}
	return v, nil
}
