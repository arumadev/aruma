package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// Replayer sends corpus requests to a live server as they were recorded, byte for byte apart from
// the project id: no SDK in between, so nothing rewrites a request (the Go SDK turns == NaN into
// IS_NAN, for one).
type Replayer struct {
	Target *conformance.HTTPTarget
}

// project replaces the recorded {project} with the live server's project.
func (r Replayer) project(s string) string {
	return strings.ReplaceAll(s, "projects/{project}/", "projects/"+r.Target.Project()+"/")
}

// Setup writes a file's fixture documents through UpdateDocument (PATCH, an upsert).
func (r Replayer) Setup(ctx context.Context, f ReplayFile) error {
	for _, w := range f.Setup {
		res, err := r.Target.Send(ctx, conformance.Request{Method: "PATCH", Path: r.project(w.Name), Body: map[string]any{"fields": w.Fields}})
		if err != nil {
			return err
		}
		if res.HTTP != 200 {
			return fmt.Errorf("%s: setup PATCH %s: HTTP %d %.300v", f.Name, w.Name, res.HTTP, res.Body)
		}
	}
	return nil
}

// Replay sends one recorded request, optionally with another body (a shrunk one).
func (r Replayer) Replay(ctx context.Context, c ReplayCase, body any) (conformance.Response, error) {
	req := conformance.Request{Method: c.Method, Path: r.project(c.Path)}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return conformance.Response{}, err
		}
		var v any
		if err := json.Unmarshal([]byte(r.project(string(b))), &v); err != nil {
			return conformance.Response{}, err
		}
		req.Body = v
	}
	return r.Target.Send(ctx, req)
}

// Compare replays one case (with body, or a shrunk one) and lists how the normalised answer
// differs from production's; empty when it does not.
func (r Replayer) Compare(ctx context.Context, f ReplayFile, c ReplayCase, body any) []string {
	res, err := r.Replay(ctx, c, body)
	if err != nil {
		return []string{err.Error()}
	}
	d := Diff(Stable(c.Response), Stable(Normalise(res.Body, r.Target.Project(), f.Project)))
	if res.HTTP != c.HTTP {
		d = append([]string{fmt.Sprintf("HTTP %d, want %d", res.HTTP, c.HTTP)}, d...)
	}
	return d
}
